// Story 4.1 QA Round 1 fixes — adapter-side coverage for blind-spot gaps
// surfaced by QA Turing.
//
// Scenarios:
//
//   - 4.1-BLIND-ERROR-004 (P1, M2 Architect Round 2 ruling) — upstream 429
//     → upstream_error_kind="quota_exhausted" slog disambiguation. INT-004
//     (401 → auth_revoked) is its 401 sibling; this test pins the 429 case.
//   - 4.1-BLIND-BOUNDARY-001 (P1) — empty messages array adapter
//     defence-in-depth: when the gateway-side Story 3.3 validation is
//     somehow bypassed (regression / direct adapter call from a future
//     internal client), the adapter must still emit a meaningful error
//     rather than POSTing an empty body to the upstream.
package internal

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	"golang.org/x/net/http2"
)

// 4.1-BLIND-ERROR-004 (P1) — upstream 429 → adapter slog
// `upstream_error_kind="quota_exhausted"` (M2 disambiguation). The
// user-facing envelope is BR-1.4 502 + 502_upstream_unavailable; this
// test asserts the *operational* disambiguation that lets oncall route
// 429 to the quota-management runbook (vs 401 which routes to key-rotation).
func TestChat_NonStreaming_Upstream_429_MapsToUnavailable_QuotaExhausted(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota_exhausted"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"quota_exhausted"`) {
		t.Fatalf("BLIND-ERROR-004 M2: slog missing upstream_error_kind=quota_exhausted; logs: %s", buf.String())
	}
}

// 4.1-BLIND-ERROR-004 streaming sibling — same 429 disambiguation on the
// pre-stream upstream-error path (gateway-side maps to JSON envelope per
// BR-2.5).
func TestChat_Streaming_Upstream_429_MapsToUnavailable_QuotaExhausted(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota_exhausted"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"quota_exhausted"`) {
		t.Fatalf("BLIND-ERROR-004 streaming M2: slog missing upstream_error_kind=quota_exhausted; logs: %s", buf.String())
	}
}

// 4.1-BLIND-BOUNDARY-001 (P1) — empty messages array: the upstream
// receives an empty messages JSON array and returns a 400-class response,
// which the adapter surfaces as CodeUnavailable + upstream_4xx slog kind
// per BR-1.4. This documents the existing fall-through behaviour (the
// adapter does NOT add a parallel pre-flight reject — keeping it simple
// per Architect Round 2 OQ7 "adapter is a thin translator"), so future
// reviewers can rely on the gateway-side validateChatRequest as the
// authoritative empty-messages defence.
func TestChat_NonStreaming_EmptyMessages_FallsThroughToUpstream4xx(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"messages cannot be empty"}}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{}, // empty — gateway validation bypassed
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable (BR-1.4 4xx mapping)", err)
	}
}
