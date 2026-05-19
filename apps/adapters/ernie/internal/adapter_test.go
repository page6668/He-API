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
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/ernie/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// canonicalErnieResponse is the JSON shape Qianfan v2 returns on the
// non-streaming happy path. Echoes OpenAI shape verbatim (OQ-4.6-3).
func canonicalErnieResponse(model, content string) []byte {
	stop := "stop"
	return mustJSON(upstream.ChatResponseJSON{
		ID:      "chatcmpl-ernie-fake-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   model,
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

// 4.6-UNIT-007 (P0) — BR-1.3 non-streaming branch emits ONE ChatChunk
// with Usage post-Normaliser + FinishReason populated; Model = "ernie-4.0".
func TestChat_NonStreaming_SingleTerminalChunk_Ernie40(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalErnieResponse("ernie-4.0", "Hello back"))
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:       "ernie-4.0",
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
	if c.Model != "ernie-4.0" {
		t.Fatalf("model = %q, want ernie-4.0 (BR-1.10 verbatim)", c.Model)
	}
	if c.Usage == nil || c.Usage.GetPromptTokens() != 7 || c.Usage.GetCompletionTokens() != 12 || c.Usage.GetTotalTokens() != 19 {
		t.Fatalf("usage = %#v, want {7,12,19}", c.Usage)
	}
	if c.GetFinishReason() != "stop" {
		t.Fatalf("finish_reason = %q, want stop", c.GetFinishReason())
	}
}

// 4.6-BLIND-CONCURRENCY-001 (P0) — parallel ernie-4.0 calls must NOT
// cross-contaminate state. Run under `-race`. Degenerate N=1 variant of
// Story-4.3's N=3 cross-contamination guard.
func TestChat_NonStreaming_Ernie40_RaceClean(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalErnieResponse(body.Model, "ok"))
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newCaptureStream()
			if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    "ernie-4.0",
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}, s); err != nil {
				t.Errorf("ernie-4.0: %v", err)
				return
			}
			if len(s.sent) != 1 || s.sent[0].Model != "ernie-4.0" {
				t.Errorf("ernie-4.0: cross-contamination; got %#v", s.sent)
			}
		}()
	}
	wg.Wait()
}

// 4.6-UNIT-015 (P0) — BR-1.9 PII-safe logs. Adapter slog records MUST
// NOT contain request `messages` content.
func TestChat_NonStreaming_PIIsafeLogs(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalErnieResponse("ernie-4.0", "ok"))
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"ernie-4.0"})

	stream := newCaptureStream()
	const secret = "PII-SECRET-ERNIE-PHRASE-DO-NOT-LOG"
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "ernie-4.0",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: secret}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("slog logs leaked PII; logs: %s", buf.String())
	}
	for _, key := range []string{`"event"`, `"model"`, `"messages_count"`, `"prompt_tokens"`} {
		if !strings.Contains(buf.String(), key) {
			t.Fatalf("missing log key %s in %s", key, buf.String())
		}
	}
}

// 4.6-UNIT-016 (P1) — m1 + m-2 cascade: adapter MUST capture upstream
// response's `x-bce-request-id` header (Baidu BCE platform convention
// per m-2 SM-leaned default; pending Phase 0 empirical-verify) and emit
// it as slog `upstream_request_id` on the adapter_chat_request_end log.
func TestChat_NonStreaming_CapturesUpstreamRequestID_m1(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Bce-Request-Id", "bce_abc123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalErnieResponse("ernie-4.0", "ok"))
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"ernie-4.0"})

	stream := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "ernie-4.0",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !strings.Contains(buf.String(), `"upstream_request_id":"bce_abc123"`) {
		t.Fatalf("m1: slog missing upstream_request_id; logs: %s", buf.String())
	}
}

// 4.6-INT-004 (P0) — upstream 429 (rate-limit) → connect.CodeUnavailable
// + slog upstream_error_kind=rate_limit_throttle (BR-4.4 cascade REUSE
// from Story-4.2).
func TestChat_NonStreaming_Upstream_429_MapsToRateLimitThrottle(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"ernie-4.0"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "ernie-4.0", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// 4.6-INT-005 (P0) — upstream 401 → connect.CodeUnavailable + slog
// upstream_error_kind=auth_revoked.
func TestChat_NonStreaming_Upstream_401_MapsToAuthRevoked(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_api_key"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"ernie-4.0"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "ernie-4.0", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"auth_revoked"`) {
		t.Fatalf("slog missing upstream_error_kind=auth_revoked; logs: %s", buf.String())
	}
}

// 4.6-INT-003 — upstream 5xx → connect.CodeUnavailable.
func TestChat_NonStreaming_Upstream5xx_MapsToUnavailable(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream incident"}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "ernie-4.0", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.6-INT-012 (P0) — BR-3.4 missing-usage HARD failure.
func TestChat_NonStreaming_MissingUsage_HardFailure(t *testing.T) {
	stop := "stop"
	body := mustJSON(upstream.ChatResponseJSON{
		ID: "chatcmpl-x", Object: "chat.completion", Created: 1, Model: "ernie-4.0",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
		// usage intentionally omitted
	})
	fake := newFakeUpstream(t, http.StatusOK, body)
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "ernie-4.0", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing_usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.6-UNIT-009 — BR-1.8 context-deadline propagation.
func TestChat_NonStreaming_ContextDeadlinePropagates(t *testing.T) {
	hang := make(chan struct{})
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		<-hang
	})
	defer fake.Close()
	defer close(hang)
	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	stream := newCaptureStream()
	err := svc.ChatInto(ctx, &adapterv1.ChatRequest{
		Model:    "ernie-4.0",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("err = nil, want deadline error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed %v > 2s — deadline not propagated", elapsed)
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Code() != connect.CodeDeadlineExceeded {
			t.Fatalf("connect code = %s, want DeadlineExceeded", connectErr.Code())
		}
	}
}

// 4.6-INT-006 (streaming-emits-chunks-with-terminal-usage) — BR-2.4
// streaming branch emits N chunks; terminal chunk carries usage
// post-Normaliser.
func TestChat_Streaming_EmitsChunksWithTerminalUsage(t *testing.T) {
	sseBody := "data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "ernie-4.0",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Role: ptr("assistant")}}},
	})) + "\n\n" +
		"data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "ernie-4.0",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hello")}}},
	})) + "\n\n" +
		"data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "ernie-4.0",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, FinishReason: ptr("stop")}},
		Usage:   &upstream.RawUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	})) + "\n\n" +
		"data: [DONE]\n\n"

	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	})
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "ernie-4.0",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:   true,
	}, stream)
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if len(stream.sent) != 3 {
		t.Fatalf("got %d chunks, want 3 (role-delta + content-delta + terminal-with-usage)", len(stream.sent))
	}
	terminal := stream.sent[len(stream.sent)-1]
	if terminal.Usage == nil || terminal.Usage.GetPromptTokens() != 3 || terminal.Usage.GetTotalTokens() != 8 {
		t.Fatalf("terminal chunk usage missing or wrong: %#v", terminal.Usage)
	}
	if terminal.GetFinishReason() != "stop" {
		t.Fatalf("terminal finish_reason = %q, want stop", terminal.GetFinishReason())
	}
}

// 4.6-INT-012b — BR-3.4 + BR-2.4: streaming run that ended cleanly
// (io.EOF) but never produced a usage-bearing chunk → HARD failure.
func TestChat_Streaming_MissingTailUsage_IsHardFailure(t *testing.T) {
	sseBody := "data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: "ernie-4.0",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hi")}}},
		// no Usage in any chunk
	})) + "\n\n" + "data: [DONE]\n\n"

	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"ernie-4.0"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "ernie-4.0", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing tail usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.6-UNIT-bound-model-ids — NewService records bound model ids;
// BoundModelIDs() returns an independent copy. N=1 case per BR-1.10.
func TestBoundModelIDs_Defensive_Copy_N1(t *testing.T) {
	svc := NewService(nil, nil, []string{"ernie-4.0"})
	got := svc.BoundModelIDs()
	if len(got) != 1 || got[0] != "ernie-4.0" {
		t.Fatalf("BoundModelIDs = %v, want [ernie-4.0] (BR-1.10 N=1 degenerate)", got)
	}
	got[0] = "MUTATED"
	got2 := svc.BoundModelIDs()
	if got2[0] != "ernie-4.0" {
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

func ptr[T any](v T) *T { return &v }

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
// httptest self-signed cert. Uses the stdlib http.Transport per OQ-4.6-5
// (ForceAttemptHTTP2 → ALPN negotiation).
func newTestClient(baseURL string) *upstream.Client {
	c := upstream.NewClient(baseURL, "test-api-key", 5*time.Second)
	c.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	return c
}

func newServiceForTest(t *testing.T, fake *httptest.Server, modelIDs []string) *Service {
	t.Helper()
	client := newTestClient(fake.URL)
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewService(client, logger, modelIDs)
}
