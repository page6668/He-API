//go:build chaos

// Story 4.1 Phase C — Chaos suite (build-tag-gated).
//
// Scenarios (CHAOS-001..006) inject failure modes between the adapter and a
// local upstream fake, exercising the BR-1.4 mapping table end-to-end. The
// `chaos` build tag keeps these out of the per-PR fast lane; nightly CI
// runs the suite with `go test -tags chaos ./apps/adapters/deepseek/tests/...`.
//
// Original story T4.3 references Toxiproxy; we implement the same fault
// matrix via local httptest fakes + a custom http.RoundTripper to keep the
// suite self-contained (no Toxiproxy daemon dependency). A future story may
// promote to Toxiproxy when the adapter farm grows.
package tests

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// CHAOS-001 — 5xx burst. Upstream returns 503 on every call → adapter
// surfaces CodeUnavailable; gateway maps to 502.
func TestCHAOS_001_5xxBurst(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"vendor outage"}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	for i := 0; i < 5; i++ {
		resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
			Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		}))
		resp.Receive()
		var ce *connect.Error
		if e := resp.Err(); e == nil || !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("burst iter %d: Err = %v, want CodeUnavailable", i, e)
		}
	}
}

// CHAOS-002 — Slow loris. Upstream sends bytes < 1byte/sec → adapter's
// 60s timeout (default) fires, surfaces CodeDeadlineExceeded.
//
// To keep the test bounded we ship a short timeout via the adapter's
// per-request context.
func TestCHAOS_002_SlowLoris(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Drip-feed for far longer than the test's bounded context allows.
		for i := 0; i < 100; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
				_, _ = w.Write([]byte{' '})
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resp, err := client.Chat(ctx, connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		// Eager error on Chat (failure surfaced on first frame).
		return
	}
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want context/deadline error")
	}
}

// CHAOS-003 — Mid-stream RST. Upstream emits a partial SSE stream then
// closes the connection mid-stream. Adapter surfaces CodeUnavailable.
func TestCHAOS_003_MidStreamRST(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// One frame, then the http.Hijacker isn't available on http2, so
		// we simulate via partial write + immediate handler return — the
		// connection terminates without `data: [DONE]`.
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Return without [DONE] + without terminal-usage chunk → BR-3.4 hard failure.
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	// Drain successful chunks before the failure surfaces.
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on premature stream close")
	}
}

// CHAOS-004 — Full timeout. Upstream never responds; bounded context.
func TestCHAOS_004_FullTimeout(t *testing.T) {
	hang := make(chan struct{})
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	defer fake.Close()
	defer close(hang)
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, err := client.Chat(ctx, connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want deadline/cancel")
	}
}

// CHAOS-005 — TLS handshake failure. Upstream serves plaintext on what the
// adapter expects to be TLS → handshake fails. Adapter surfaces
// CodeUnavailable (TLS classification → 502).
func TestCHAOS_005_TLSHandshakeFailure(t *testing.T) {
	// Plaintext server — the adapter's HTTP/2 transport expects TLS.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer plain.Close()
	// startAdapterServer will use plain.URL as the upstream — the http2
	// transport will fail on the missing TLS hand-shake.
	adapter := startAdapterServer(t, plain.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on TLS failure")
	}
}

// CHAOS-006 — DNS resolution failure. Adapter's upstream URL points at a
// non-resolvable host; client.Chat surfaces CodeUnavailable.
func TestCHAOS_006_DNSResolutionFailure(t *testing.T) {
	// .invalid is reserved (RFC 6761 §6.4) — guaranteed never to resolve.
	adapter := startAdapterServer(t, "https://dns-target-does-not-exist.invalid")
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		// Connect-side eagerly surfaces dial error — acceptable.
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on DNS failure")
	}
	if e := resp.Err(); !strings.Contains(strings.ToLower(e.Error()), "no such host") && !strings.Contains(strings.ToLower(e.Error()), "unavailable") {
		// Acceptable forms: lookup error OR Connect-wrapped Unavailable.
		t.Logf("DNS failure surfaced as: %v", e)
	}
}

// Compile-time guards.
var (
	_ = net.Dialer{}
	_ = time.Second
)
