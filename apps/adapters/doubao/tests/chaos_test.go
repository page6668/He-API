//go:build chaos

// Story 4.5 Phase 5 — Doubao (Volcengine Ark v3) adapter Chaos suite
// (build-tag-gated).
//
// Scenarios CHAOS-001..008 inject failure modes between the adapter and
// a local upstream fake, exercising the BR-1.4 + BR-4.4 + NEW BR-4.6
// mapping table end-to-end. The `chaos` build tag keeps these out of the
// per-PR fast lane; nightly CI runs the suite with
// `go test -tags chaos ./apps/adapters/doubao/tests/...`.
//
// Story-4.5-specific notes:
//   - CHAOS-007: Volcengine-flavoured 429 body (REUSE Story-4.2 OQ-4.2-4
//     cascade → BR-4.4 rate_limit_throttle slog kind).
//   - CHAOS-008: NEW for Story 4.5 — endpoint-id-not-configured fail-fast
//     (BR-1.12 + BR-4.6). Spawn the adapter with DOUBAO_PRO_ENDPOINT_ID
//     empty; verify adapter Connect-RPC returns CodeFailedPrecondition
//     and upstream is NEVER called.
//   - NO body-aware classifier — OQ-4.5-6 verbatim REUSE.
//
// Original story T4.1 references Toxiproxy; we implement the same fault
// matrix via local httptest fakes to keep the suite self-contained.
package tests

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	doubaointernal "github.com/he-api/he-api/apps/adapters/doubao/internal"
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// CHAOS-001 — 5xx burst.
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
			Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		}))
		resp.Receive()
		var ce *connect.Error
		if e := resp.Err(); e == nil || !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("burst iter %d: Err = %v, want CodeUnavailable", i, e)
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
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"" + testProEndpointID + "\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
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
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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
	adapter := startAdapterServer(t, "https://dns-target-does-not-exist-doubao.invalid")
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// CHAOS-007 — Volcengine-flavoured 429 rate-limit shape (REUSE Story-4.2
// OQ-4.2-4 cascade).
//
// Architect Round 1 BR-4.4: upstream 429 → CodeUnavailable (mapped to
// 502 by gateway BR-1.4); slog carries `upstream_error_kind=rate_limit_throttle`
// for ops triage.
func TestCHAOS_007_Doubao_429_RateLimitThrottle(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded for doubao-pro endpoint"}}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// CHAOS-008 — NEW for Story 4.5: endpoint-id-not-configured fail-fast.
// Spawn the adapter with DOUBAO_PRO_ENDPOINT_ID empty → adapter Chat
// returns CodeFailedPrecondition per BR-1.12; upstream HTTPS NEVER called.
// Adapter slog carries upstream_error_kind=endpoint_id_not_configured per BR-4.6.
//
// Symmetric assertion for doubao-lite.
func TestCHAOS_008_EndpointIDNotConfigured_FailFast(t *testing.T) {
	var upstreamCalls atomic.Int32
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":"should not reach"}`))
	})
	defer fake.Close()

	// Spawn an adapter whose EndpointMap returns empty for BOTH model ids.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	upstreamClient := upstream.NewClient(fake.URL, "test-api-key", 5*time.Second)
	upstreamClient.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	em := upstream.New(func(string) string { return "" }) // BR-1.12 fail-fast trigger
	svc := doubaointernal.NewService(upstreamClient, em, logger, []string{"doubao-pro", "doubao-lite"})

	mux := http.NewServeMux()
	path, handler := adapterv1connect.NewAdapterServiceHandler(svc)
	mux.Handle(path, handler)
	adapter := httptest.NewServer(mux)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	for _, modelID := range []string{"doubao-pro", "doubao-lite"} {
		resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
			Model: modelID, Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		}))
		resp.Receive()
		e := resp.Err()
		if e == nil {
			t.Fatalf("%s: Err = nil, want CodeFailedPrecondition (BR-1.12)", modelID)
		}
		var ce *connect.Error
		if !errors.As(e, &ce) || ce.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("%s: Err = %v, want CodeFailedPrecondition", modelID, e)
		}
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream was called %d times; want 0 (fail-fast must NOT initiate HTTPS call)", got)
	}
	if !strings.Contains(logBuf.String(), `"upstream_error_kind":"endpoint_id_not_configured"`) {
		t.Fatalf("BR-4.6: slog missing upstream_error_kind=endpoint_id_not_configured; logs: %s", logBuf.String())
	}
}

// Compile-time guards.
var (
	_ = net.Dialer{}
	_ = time.Second
)
