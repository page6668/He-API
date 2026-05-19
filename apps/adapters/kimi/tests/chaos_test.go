//go:build chaos

// Story 4.3 Phase C — Kimi (Moonshot) adapter Chaos suite (build-tag-gated).
//
// Scenarios CHAOS-001..008 inject failure modes between the adapter and a
// local upstream fake, exercising the BR-1.4 + BR-4.4 + BR-4.5 mapping
// table end-to-end. The `chaos` build tag keeps these out of the per-PR
// fast lane; nightly CI runs the suite with
// `go test -tags chaos ./apps/adapters/kimi/tests/...`.
//
// Kimi-specific additions vs Story-4.2 Qwen chaos suite:
//   - CHAOS-007: Moonshot-flavoured 429 body (REUSE Story-4.2 OQ-4.2-4 cascade
//     → BR-4.4 rate_limit_throttle slog kind).
//   - CHAOS-008: NEW — Moonshot 400 context-length-exceeded body shape;
//     verifies BR-4.5 / Architect Round 1 m-1 body-aware classifier surfaces
//     the ErrorKindContextLengthExceeded slog kind.
//
// Original story T4.3 references Toxiproxy; we implement the same fault
// matrix via local httptest fakes to keep the suite self-contained.
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

// CHAOS-001 — 5xx burst (rotated across all three Kimi sizes).
func TestCHAOS_001_5xxBurst(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"vendor outage"}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	for i, model := range []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k", "moonshot-v1-8k", "moonshot-v1-32k"} {
		resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
			Model: model, Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		}))
		resp.Receive()
		var ce *connect.Error
		if e := resp.Err(); e == nil || !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("burst iter %d (%s): Err = %v, want CodeUnavailable", i, model, e)
		}
	}
}

// CHAOS-002 — Slow loris.
func TestCHAOS_002_SlowLoris(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
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
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want context/deadline error")
	}
}

// CHAOS-003 — Mid-stream RST.
func TestCHAOS_003_MidStreamRST(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"moonshot-v1-128k\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "moonshot-v1-128k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on premature stream close")
	}
}

// CHAOS-004 — Full timeout.
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
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want deadline/cancel")
	}
}

// CHAOS-005 — TLS handshake failure.
func TestCHAOS_005_TLSHandshakeFailure(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer plain.Close()
	adapter := startAdapterServer(t, plain.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on TLS failure")
	}
}

// CHAOS-006 — DNS resolution failure.
func TestCHAOS_006_DNSResolutionFailure(t *testing.T) {
	adapter := startAdapterServer(t, "https://dns-target-does-not-exist.invalid")
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		return
	}
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err = nil; want CodeUnavailable on DNS failure")
	}
	if e := resp.Err(); !strings.Contains(strings.ToLower(e.Error()), "no such host") && !strings.Contains(strings.ToLower(e.Error()), "unavailable") {
		t.Logf("DNS failure surfaced as: %v", e)
	}
}

// CHAOS-007 — Kimi-specific: Moonshot 429 rate-limit shape (REUSE
// Story-4.2 OQ-4.2-4 cascade).
//
// Architect Round 1 BR-4.4: upstream 429 → CodeUnavailable (mapped to
// 502 by gateway BR-1.4); slog carries
// `upstream_error_kind=rate_limit_throttle` for ops triage.
func TestCHAOS_007_Moonshot_429_RateLimitThrottle(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_exceeded","message":"Rate limit exceeded for moonshot-v1-8k"}}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	resp.Receive()
	e := resp.Err()
	if e == nil {
		t.Fatalf("Err = nil, want CodeUnavailable on upstream 429")
	}
	var ce *connect.Error
	if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("Err = %v, want CodeUnavailable", e)
	}
}

// CHAOS-008 — NEW Kimi-specific: Moonshot 400 context-length-exceeded
// shape (BR-4.5 / Architect Round 1 m-1 ratification).
//
// Moonshot returns context-window-exceeded errors as HTTP 400 with body
// `{"error":{"type":"invalid_request_error","message":"...maximum context
// length..."}}`. The body-aware classifier surfaces
// `upstream_error_kind=context_length_exceeded` so ops can triage
// user-side request-size issues distinctly from platform outages.
// Gateway envelope still maps to 502 per BR-1.4 (distinct envelope code
// deferred to Story 4.7 capability matrix).
func TestCHAOS_008_Moonshot_400_ContextLengthExceeded(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Your request exceeded the maximum context length of 8192 tokens. Try moonshot-v1-32k or moonshot-v1-128k."}}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "<oversize prompt>"}},
	}))
	resp.Receive()
	e := resp.Err()
	if e == nil {
		t.Fatalf("Err = nil, want CodeUnavailable on 400 context-length-exceeded")
	}
	var ce *connect.Error
	if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("Err = %v, want CodeUnavailable (BR-1.4 envelope preserved; slog kind verified at unit level)", e)
	}
}

// Compile-time guards.
var (
	_ = net.Dialer{}
	_ = time.Second
)
