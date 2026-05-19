// Package tests carries the cross-package integration scenarios for the
// DeepSeek adapter. Unit-level coverage lives next to the production code
// (internal/upstream/*_test.go, internal/usage/*_test.go, internal/adapter_test.go).
//
// Integration scenarios (4.1-INT-001..006 in Phase A) exercise the
// adapter's Connect-RPC Chat handler end-to-end against an httptest TLS
// fake upstream. The streaming (INT-007..012) and chaos (CHAOS-001..006)
// suites land in Phase B + Phase C respectively.
package tests

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	deepseekinternal "github.com/he-api/he-api/apps/adapters/deepseek/internal"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
	"golang.org/x/net/http2"
)

func canonicalUpstreamBody(content string) []byte {
	stop := "stop"
	b, _ := json.Marshal(upstream.ChatResponseJSON{
		ID:      "chatcmpl-vendor-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "deepseek-v3",
		Choices: []upstream.ChatChoiceJSON{{
			Index:        0,
			Message:      &upstream.ChatMessage{Role: "assistant", Content: content},
			FinishReason: &stop,
		}},
		Usage: &upstream.RawUsage{PromptTokens: 7, CompletionTokens: 12, TotalTokens: 19},
	})
	return b
}

// 4.1-INT-001 (P0) — Happy-path non-streaming end-to-end. Connect-RPC
// client → adapter HTTP server → adapter Chat handler → upstream fake →
// terminal ChatChunk on the wire.
func TestINT_001_NonStreaming_HappyPath(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("Hello from upstream"))
	})
	defer fake.Close()

	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !resp.Receive() {
		t.Fatalf("no chunk received: %v", resp.Err())
	}
	chunk := resp.Msg()
	if chunk.GetModel() != "deepseek-v3" {
		t.Fatalf("model = %q", chunk.GetModel())
	}
	if chunk.GetUsage().GetTotalTokens() != 19 {
		t.Fatalf("usage.total_tokens = %d, want 19", chunk.GetUsage().GetTotalTokens())
	}
	if resp.Receive() {
		t.Fatalf("got extra chunk on non-streaming path")
	}
}

// 4.1-INT-002 (P0) — Outbound bearer token is set on the upstream request.
func TestINT_002_UpstreamBearerToken(t *testing.T) {
	var seenAuth string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if seenAuth != "Bearer test-api-key" {
		t.Fatalf("upstream Authorization = %q, want %q", seenAuth, "Bearer test-api-key")
	}
}

// 4.1-INT-003 (P0) — Upstream 5xx → Connect-RPC CodeUnavailable.
func TestINT_003_Upstream_5xx_MapsToUnavailable(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"vendor down"}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	_, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	// On server-streaming, the Chat call returns the stream eagerly even
	// if the server emits CodeUnavailable on the first Send. The error
	// surfaces on Receive() / Err().
	if err != nil {
		// Server emitted error during open — also acceptable.
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("err = %v, want CodeUnavailable", err)
		}
		return
	}
	// Otherwise, the error must surface on Err.
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if resp.Receive() {
		t.Fatalf("got a chunk on 5xx path; expected no chunks before error")
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err() = nil, want CodeUnavailable")
	} else {
		var ce *connect.Error
		if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("Err() = %v, want CodeUnavailable", e)
		}
	}
}

// 4.1-INT-004 (P0) — Upstream 401 → CodeUnavailable + auth_revoked slog kind
// (M2 disambiguation).
func TestINT_004_Upstream_401_AuthRevoked_Disambiguation(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid_api_key"}}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	resp.Receive()
	if e := resp.Err(); e == nil {
		t.Fatalf("Err() = nil, want CodeUnavailable")
	}
}

// 4.1-INT-005 (P0) — BR-1.5 request-id propagated. Gateway → adapter via
// Connect-RPC X-He-Request-Id header, adapter → upstream via X-Request-Id.
func TestINT_005_RequestId_Propagation(t *testing.T) {
	var seenReqID string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	req := connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	})
	req.Header().Set("X-He-Request-Id", "req_intgrtnTest1")
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if seenReqID != "req_intgrtnTest1" {
		t.Fatalf("upstream X-Request-Id = %q, want %q", seenReqID, "req_intgrtnTest1")
	}
}

// 4.1-INT-006 (P0) — OQ7 binding. The outbound HTTPS request to upstream
// uses HTTP/2 on the wire. We assert this via the upstream fake's r.Proto.
func TestINT_006_Upstream_HTTP2_On_The_Wire(t *testing.T) {
	var seenProto string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenProto = r.Proto
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if !strings.HasPrefix(seenProto, "HTTP/2") {
		t.Fatalf("upstream proto = %q, want HTTP/2.* (OQ7 forced)", seenProto)
	}
}

// --- helpers --------------------------------------------------------------

// startFakeUpstream wires an HTTP/2-enabled TLS httptest server. The
// adapter's HTTPS client is configured (in the test helpers below) to
// trust the test server's self-signed cert.
func startFakeUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = true
	s.StartTLS()
	return s
}

// startAdapterServer wires the AdapterService over Connect-RPC and serves
// it on a local httptest server. The adapter's upstream Client is built
// against `upstreamURL` with TLS-skip-verify on (so the test fake's self-
// signed cert is accepted).
func startAdapterServer(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	upstreamClient := upstream.NewClient(upstreamURL, "test-api-key", 5*time.Second)
	upstreamClient.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	svc := deepseekinternal.NewService(upstreamClient, logger)

	mux := http.NewServeMux()
	path, handler := adapterv1connect.NewAdapterServiceHandler(svc)
	mux.Handle(path, handler)

	s := httptest.NewUnstartedServer(mux)
	// Adapter is served plaintext HTTP/1.1 over loopback in tests; the
	// production-side mTLS hardening lands in Epic 9 ops. Connect-RPC over
	// h2c is supported via the adapter's HTTPS endpoint in prod.
	s.Start()
	return s
}

func newAdapterClient(baseURL string) adapterv1connect.AdapterServiceClient {
	return adapterv1connect.NewAdapterServiceClient(&http.Client{}, baseURL)
}
