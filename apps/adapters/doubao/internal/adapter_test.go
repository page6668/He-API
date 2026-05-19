package internal

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// canonicalDoubaoResponse is the JSON shape Volcengine Ark v3 returns on
// the non-streaming happy path. Echoes OpenAI shape verbatim per OQ-4.5-3
// for non-`model` fields; `Model` field carries the Volcengine endpoint id
// (NOT the friendly id) — the adapter back-translates per BR-1.11.
func canonicalDoubaoResponse(echoedEndpointID, content string) []byte {
	stop := "stop"
	return mustJSON(upstream.ChatResponseJSON{
		ID:      "chatcmpl-doubao-fake-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   echoedEndpointID,
		Choices: []upstream.ChatChoiceJSON{{
			Index:        0,
			Message:      &upstream.ChatMessage{Role: "assistant", Content: content},
			FinishReason: &stop,
		}},
		Usage: &upstream.RawUsage{PromptTokens: 7, CompletionTokens: 12, TotalTokens: 19},
	})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// 4.5-INT-001-internal (P0) — BR-1.3 non-streaming branch emits ONE
// ChatChunk with Usage post-Normaliser + FinishReason populated; AND
// BR-1.11 inbound back-translate — chunk.Model = friendly id (NOT the
// upstream-echoed endpoint id).
func TestChat_NonStreaming_SingleTerminalChunk_DoubaoPro_BackTranslated(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalDoubaoResponse("ep-test-pro-001", "Hello back"))
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:       "doubao-pro",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:      false,
		HeRequestId: "req_111111111111",
	}, stream)
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d chunks, want 1", len(stream.sent))
	}
	c := stream.sent[0]
	if c.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", c.Object)
	}
	if c.Model != "doubao-pro" {
		t.Fatalf("model = %q, want doubao-pro (BR-1.11 inbound back-translate; MUST NOT be endpoint id)", c.Model)
	}
	if c.Model == "ep-test-pro-001" {
		t.Fatalf("model = %q leaked endpoint id; back-translate regression", c.Model)
	}
	if c.Usage == nil || c.Usage.GetPromptTokens() != 7 || c.Usage.GetCompletionTokens() != 12 || c.Usage.GetTotalTokens() != 19 {
		t.Fatalf("usage = %#v, want {7,12,19}", c.Usage)
	}
	if c.GetFinishReason() != "stop" {
		t.Fatalf("finish_reason = %q, want stop", c.GetFinishReason())
	}
}

// 4.5-INT-002-internal (P0) — outbound HTTPS body's model field is the
// Volcengine endpoint id (NOT the friendly id); verifies BR-1.7.f
// outbound rewrite at the integration boundary.
func TestChat_NonStreaming_OutboundBody_ModelIsEndpointID(t *testing.T) {
	var seenOutboundModel string
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenOutboundModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalDoubaoResponse(body.Model, "ok"))
	})
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")
	stream := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "doubao-lite", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if seenOutboundModel != "ep-test-lite-001" {
		t.Fatalf("upstream received model = %q, want ep-test-lite-001 (BR-1.7.f outbound rewrite for doubao-lite)", seenOutboundModel)
	}
	// And verify the back-translate on the return side too.
	if len(stream.sent) == 0 || stream.sent[0].Model != "doubao-lite" {
		t.Fatalf("returned chunk model = %v, want doubao-lite", stream.sent)
	}
}

// 4.5-INT-008-internal (P0 in chain) — Endpoint-id fail-fast: with env
// vars unset, ChatInto returns connect.CodeFailedPrecondition per BR-1.12;
// upstream HTTPS call is NEVER initiated (no requests recorded).
//
// Also covers 4.5-CHAOS-008 partial scenario (the per-request flavour).
func TestChat_NonStreaming_EndpointIDNotConfigured_FailFast(t *testing.T) {
	var upstreamCalls int
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalDoubaoResponse("ep-unused", "should not reach"))
	})
	defer fake.Close()

	// Both endpoint env vars empty → BR-1.12 fail-fast.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	em := upstream.New(func(string) string { return "" })
	svc := NewService(client, em, logger, []string{"doubao-pro", "doubao-lite"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want CodeFailedPrecondition (BR-1.12 fail-fast)")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want connect.CodeFailedPrecondition (BR-1.12)", err)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream was called %d times; want 0 (fail-fast must NOT invoke upstream)", upstreamCalls)
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"endpoint_id_not_configured"`) {
		t.Fatalf("BR-4.6: slog missing upstream_error_kind=endpoint_id_not_configured; logs: %s", buf.String())
	}
}

// 4.5-UNIT-015 (P0) — BR-1.9 EXPANDED PII discipline: adapter slog
// records MUST NOT contain request `messages` content NOR endpoint-id
// values (ep-* substrings). Companion regex coverage in 4.5-BLIND-TRANSLATE-002.
func TestChat_NonStreaming_PIIsafeLogs_ExcludeMessagesAndEndpointIDs(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalDoubaoResponse("ep-canary-pro-LEAK", "ok"))
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	em := upstream.New(func(name string) string {
		switch name {
		case "DOUBAO_PRO_ENDPOINT_ID":
			return "ep-canary-pro-LEAK"
		case "DOUBAO_LITE_ENDPOINT_ID":
			return "ep-canary-lite-LEAK"
		}
		return ""
	})
	svc := NewService(client, em, logger, []string{"doubao-pro", "doubao-lite"})

	stream := newCaptureStream()
	const secret = "SECRET_LEAK_CANARY_4_5"
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: secret}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, secret) {
		t.Fatalf("slog logs leaked PII canary %q; logs: %s", secret, logs)
	}
	if strings.Contains(logs, "ep-canary-pro-LEAK") || strings.Contains(logs, "ep-canary-lite-LEAK") {
		t.Fatalf("slog logs leaked endpoint-id canary; logs: %s", logs)
	}
	// 4.5-BLIND-TRANSLATE-002 — regex guard for any ep-* shape.
	leakRegex := regexp.MustCompile(`ep-[A-Za-z0-9-]{8,}`)
	if leakRegex.MatchString(logs) {
		t.Fatalf("slog logs leaked endpoint-id-shaped substring (regex guard); logs: %s", logs)
	}
	// Allow-list verified.
	for _, key := range []string{`"event"`, `"model"`, `"messages_count"`, `"prompt_tokens"`} {
		if !strings.Contains(logs, key) {
			t.Fatalf("missing log key %s in %s", key, logs)
		}
	}
}

// 4.5-UNIT-016 (P1) — m1 cascade: adapter MUST capture upstream response's
// X-Request-Id header and emit it as slog `upstream_request_id` on the
// adapter_chat_request_end log.
func TestChat_NonStreaming_CapturesUpstreamRequestID_m1Cascade(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "volc_abc123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalDoubaoResponse("ep-test-pro-001", "ok"))
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	em := upstream.New(staticEnv("ep-test-pro-001", "ep-test-lite-001"))
	svc := NewService(client, em, logger, []string{"doubao-pro", "doubao-lite"})

	stream := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !strings.Contains(buf.String(), `"upstream_request_id":"volc_abc123"`) {
		t.Fatalf("m1: slog missing upstream_request_id; logs: %s", buf.String())
	}
}

// 4.5-INT-004-internal (P0) — upstream 429 → connect.CodeUnavailable +
// slog upstream_error_kind=rate_limit_throttle (BR-4.4 cascade REUSE).
func TestChat_NonStreaming_Upstream429_MapsToRateLimitThrottle(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	em := upstream.New(staticEnv("ep-test-pro-001", "ep-test-lite-001"))
	svc := NewService(client, em, logger, []string{"doubao-pro", "doubao-lite"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"rate_limit_throttle"`) {
		t.Fatalf("BR-4.4: slog missing upstream_error_kind=rate_limit_throttle; logs: %s", buf.String())
	}
}

// 4.5-INT-005-internal (P0) — upstream 401 → CodeUnavailable + slog
// upstream_error_kind=auth_revoked.
func TestChat_NonStreaming_Upstream401_MapsToAuthRevoked(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_key"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	em := upstream.New(staticEnv("ep-test-pro-001", "ep-test-lite-001"))
	svc := NewService(client, em, logger, []string{"doubao-pro", "doubao-lite"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"auth_revoked"`) {
		t.Fatalf("slog missing upstream_error_kind=auth_revoked; logs: %s", buf.String())
	}
}

// 4.5-INT-003-internal (P0) — upstream 5xx → CodeUnavailable.
func TestChat_NonStreaming_Upstream5xx_MapsToUnavailable(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream incident"}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.5-INT-012-internal (P0) — BR-3.4 missing-usage HARD failure.
func TestChat_NonStreaming_MissingUsage_HardFailure(t *testing.T) {
	stop := "stop"
	body := mustJSON(upstream.ChatResponseJSON{
		ID: "chatcmpl-x", Object: "chat.completion", Created: 1, Model: "ep-test-pro-001",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
		// usage intentionally omitted
	})
	fake := newFakeUpstream(t, http.StatusOK, body)
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing_usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.5-UNIT-007-internal (P0) — BR-1.8 context-deadline propagation.
func TestChat_NonStreaming_ContextDeadlinePropagates(t *testing.T) {
	hang := make(chan struct{})
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		<-hang
	})
	defer fake.Close()
	defer close(hang)
	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	stream := newCaptureStream()
	err := svc.ChatInto(ctx, &adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("err = nil, want deadline error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed %v > 2s — deadline not propagated", elapsed)
	}
}

// 4.5-BLIND-CONCURRENCY-001 (P0) — N=2 RESTORED concurrency: 100 concurrent
// calls split 50:50 pro:lite mixed → no panic, no state corruption,
// race-detector clean.
func TestChat_NonStreaming_DoubaoPro_DoubaoLite_RaceClean_N2_Restored(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalDoubaoResponse(body.Model, "ok"))
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"doubao-pro", "doubao-lite"}, "ep-test-pro-001", "ep-test-lite-001")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		modelID := "doubao-pro"
		expectedEndpointID := "ep-test-pro-001"
		if i%2 == 1 {
			modelID = "doubao-lite"
			expectedEndpointID = "ep-test-lite-001"
		}
		go func(mid, epID string) {
			defer wg.Done()
			s := newCaptureStream()
			if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    mid,
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}, s); err != nil {
				t.Errorf("%s: %v", mid, err)
				return
			}
			if len(s.sent) != 1 {
				t.Errorf("%s: got %d chunks, want 1", mid, len(s.sent))
				return
			}
			if s.sent[0].Model != mid {
				t.Errorf("%s: cross-contamination; model = %q (back-translate broken? wanted endpoint id %s upstream-side)", mid, s.sent[0].Model, epID)
			}
		}(modelID, expectedEndpointID)
	}
	wg.Wait()
}

// 4.5-UNIT-bound-model-ids — NewService records bound model ids;
// BoundModelIDs returns an independent copy. N=2 case per BR-1.10.
func TestBoundModelIDs_Defensive_Copy_N2(t *testing.T) {
	svc := NewService(nil, nil, nil, []string{"doubao-pro", "doubao-lite"})
	got := svc.BoundModelIDs()
	if len(got) != 2 || got[0] != "doubao-pro" || got[1] != "doubao-lite" {
		t.Fatalf("BoundModelIDs = %v, want [doubao-pro doubao-lite] (BR-1.10 N=2)", got)
	}
	got[0] = "MUTATED"
	got2 := svc.BoundModelIDs()
	if got2[0] != "doubao-pro" {
		t.Fatalf("BoundModelIDs not defensively copied: got2[0]=%q", got2[0])
	}
}

// --- helpers --------------------------------------------------------------

type captureStream struct {
	sent []*adapterv1.ChatChunk
}

func newCaptureStream() *captureStream { return &captureStream{} }

func (c *captureStream) Send(chunk *adapterv1.ChatChunk) error {
	c.sent = append(c.sent, chunk)
	return nil
}

func staticEnv(proEPID, liteEPID string) func(string) string {
	return func(name string) string {
		switch name {
		case upstream.EnvDoubaoProEndpointID:
			return proEPID
		case upstream.EnvDoubaoLiteEndpointID:
			return liteEPID
		}
		return ""
	}
}

func newFakeUpstream(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	return newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

func newFakeUpstreamHandler(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(h))
	s.EnableHTTP2 = true
	s.StartTLS()
	return s
}

// newTestClient builds an upstream.Client whose transport accepts the
// httptest self-signed cert.
func newTestClient(baseURL string) *upstream.Client {
	c := upstream.NewClient(baseURL, "test-api-key", 5*time.Second)
	c.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return c
}

func newServiceForTest(t *testing.T, fake *httptest.Server, modelIDs []string, proEPID, liteEPID string) *Service {
	t.Helper()
	client := newTestClient(fake.URL)
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	em := upstream.New(staticEnv(proEPID, liteEPID))
	return NewService(client, em, logger, modelIDs)
}
