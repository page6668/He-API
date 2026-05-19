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
	"github.com/he-api/he-api/apps/adapters/kimi/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// canonicalKimiResponse is the JSON shape Moonshot API returns
// on the non-streaming happy path. Echoes OpenAI shape verbatim (OQ-4.2-1).
func canonicalKimiResponse(model, content string) []byte {
	stop := "stop"
	return mustJSON(upstream.ChatResponseJSON{
		ID:      "chatcmpl-kimi-fake-001",
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

// 4.3-UNIT-005 (P0) — BR-1.3 non-streaming branch emits ONE ChatChunk
// with Usage post-Normaliser + FinishReason populated; Model = moonshot-v1-8k.
func TestChat_NonStreaming_SingleTerminalChunk_KimiMax(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalKimiResponse("moonshot-v1-8k", "Hello back"))
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k", "moonshot-v1-32k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:       "moonshot-v1-8k",
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
	if c.Model != "moonshot-v1-8k" {
		t.Fatalf("model = %q, want moonshot-v1-8k (BR-1.10 verbatim)", c.Model)
	}
	if c.Usage == nil || c.Usage.GetPromptTokens() != 7 || c.Usage.GetCompletionTokens() != 12 || c.Usage.GetTotalTokens() != 19 {
		t.Fatalf("usage = %#v, want {7,12,19}", c.Usage)
	}
	if c.GetFinishReason() != "stop" {
		t.Fatalf("finish_reason = %q, want stop", c.GetFinishReason())
	}
}

// 4.3-UNIT-005b (P0) — same as above but model=moonshot-v1-32k → verifies BR-1.10
// model passthrough on the OTHER bound model id.
func TestChat_NonStreaming_SingleTerminalChunk_KimiPlus(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalKimiResponse("moonshot-v1-32k", "Plus reply"))
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k", "moonshot-v1-32k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-32k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}, stream)
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d chunks, want 1", len(stream.sent))
	}
	c := stream.sent[0]
	if c.Model != "moonshot-v1-32k" {
		t.Fatalf("model = %q, want moonshot-v1-32k", c.Model)
	}
}

// 4.3-INT-009 / BLIND-CONCURRENCY (P0) — parallel moonshot-v1-8k + moonshot-v1-32k
// requests must NOT cross-contaminate state. Run under `-race`.
func TestChat_NonStreaming_KimiMaxAndPlus_NoCrossContamination(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalKimiResponse(body.Model, "ok"))
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k", "moonshot-v1-32k"})

	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s := newCaptureStream()
			if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    "moonshot-v1-8k",
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}, s); err != nil {
				t.Errorf("moonshot-v1-8k: %v", err)
				return
			}
			if len(s.sent) != 1 || s.sent[0].Model != "moonshot-v1-8k" {
				t.Errorf("moonshot-v1-8k: cross-contamination; got %#v", s.sent)
			}
		}()
		go func() {
			defer wg.Done()
			s := newCaptureStream()
			if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    "moonshot-v1-32k",
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}, s); err != nil {
				t.Errorf("moonshot-v1-32k: %v", err)
				return
			}
			if len(s.sent) != 1 || s.sent[0].Model != "moonshot-v1-32k" {
				t.Errorf("moonshot-v1-32k: cross-contamination; got %#v", s.sent)
			}
		}()
	}
	wg.Wait()
}

// 4.3-UNIT-014 (P0) — BR-1.9 PII-safe logs. Adapter slog records MUST
// NOT contain request `messages` content.
func TestChat_NonStreaming_PIIsafeLogs(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalKimiResponse("moonshot-v1-8k", "ok"))
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"moonshot-v1-8k", "moonshot-v1-32k"})

	stream := newCaptureStream()
	const secret = "PII-SECRET-QWEN-PHRASE-DO-NOT-LOG"
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
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

// 4.3-UNIT-015 (P0) — Architect Round 1 m1: adapter MUST capture upstream
// response's X-Request-Id header and emit it as slog
// `upstream_request_id` on the adapter_chat_request_end log.
func TestChat_NonStreaming_CapturesUpstreamRequestID_m1(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-Id", "dashscope-req-XYZ123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalKimiResponse("moonshot-v1-8k", "ok"))
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"moonshot-v1-8k"})

	stream := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !strings.Contains(buf.String(), `"upstream_request_id":"dashscope-req-XYZ123"`) {
		t.Fatalf("m1: slog missing upstream_request_id; logs: %s", buf.String())
	}
}

// 4.3-INT-004 (P0) — OQ-4.2-4 ruling: upstream 429 (rate-limit) →
// connect.CodeUnavailable + slog upstream_error_kind=rate_limit_throttle.
func TestChat_NonStreaming_Upstream_429_MapsToRateLimitThrottle(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"Throttling.RateQuota"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"moonshot-v1-8k"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// 4.3-UNIT-upstream-5xx-maps-to-Unavailable
func TestChat_NonStreaming_Upstream5xx_MapsToUnavailable(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"upstream incident"}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.3-INT-012 (P0) — BR-3.4 missing-usage HARD failure.
func TestChat_NonStreaming_MissingUsage_HardFailure(t *testing.T) {
	stop := "stop"
	body := mustJSON(upstream.ChatResponseJSON{
		ID: "chatcmpl-x", Object: "chat.completion", Created: 1, Model: "moonshot-v1-8k",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
		// usage intentionally omitted
	})
	fake := newFakeUpstream(t, http.StatusOK, body)
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing_usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.3-UNIT-context-deadline — BR-1.8 context-deadline propagation.
func TestChat_NonStreaming_ContextDeadlinePropagates(t *testing.T) {
	hang := make(chan struct{})
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		<-hang
	})
	defer fake.Close()
	defer close(hang)
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	stream := newCaptureStream()
	err := svc.ChatInto(ctx, &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
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

// 4.3-UNIT-streaming-emits-multiple-chunks — BR-2.4 streaming branch
// emits N chunks; terminal chunk carries usage post-Normaliser.
func TestChat_Streaming_EmitsChunksWithTerminalUsage(t *testing.T) {
	sseBody := "data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "moonshot-v1-8k",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Role: ptr("assistant")}}},
	})) + "\n\n" +
		"data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "moonshot-v1-8k",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hello")}}},
	})) + "\n\n" +
		"data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "chatcmpl-s", Object: "chat.completion.chunk", Created: 1, Model: "moonshot-v1-8k",
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

	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
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

// 4.3-UNIT-streaming-missing-tail-usage — BR-3.4 + BR-2.4: streaming run
// that ended cleanly (io.EOF) but never produced a usage-bearing chunk →
// HARD failure.
func TestChat_Streaming_MissingTailUsage_IsHardFailure(t *testing.T) {
	sseBody := "data: " + string(mustJSON(upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: "moonshot-v1-8k",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hi")}}},
		// no Usage in any chunk
	})) + "\n\n" + "data: [DONE]\n\n"

	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing tail usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.3-UNIT-bound-model-ids — NewService records bound model ids;
// BoundModelIDs() returns an independent copy.
// Strengthened to N=3 per Story 4.3 BR-1.10 (three-size topology).
func TestBoundModelIDs_Defensive_Copy(t *testing.T) {
	svc := NewService(nil, nil, []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"})
	got := svc.BoundModelIDs()
	if len(got) != 3 || got[0] != "moonshot-v1-8k" || got[1] != "moonshot-v1-32k" || got[2] != "moonshot-v1-128k" {
		t.Fatalf("BoundModelIDs = %v, want all three Kimi sizes", got)
	}
	got[0] = "MUTATED"
	got2 := svc.BoundModelIDs()
	if got2[0] != "moonshot-v1-8k" {
		t.Fatalf("BoundModelIDs not defensively copied: got2[0]=%q", got2[0])
	}
}

// 4.3-UNIT-005c (P0) — BR-1.10 model passthrough for the THIRD size
// variant (128k). Same shape as the 8k/32k tests; demonstrates the
// N=3-per-vendor topology compiles end-to-end without per-size carve-out.
func TestChat_NonStreaming_SingleTerminalChunk_MoonshotV1_128k(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalKimiResponse("moonshot-v1-128k", "Long-doc reply"))
	defer fake.Close()

	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"})
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-128k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}, stream)
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d chunks, want 1", len(stream.sent))
	}
	if stream.sent[0].Model != "moonshot-v1-128k" {
		t.Fatalf("model = %q, want moonshot-v1-128k", stream.sent[0].Model)
	}
}

// 4.3-UNIT-005d (P0) — three-model parallel dispatch must NOT
// cross-contaminate. Strengthens the Story-4.2 N=2 test to N=3 per
// BR-1.10 / Architect Round 1 OQ-4.3-5. Run under `-race`.
func TestChat_NonStreaming_AllThreeKimiSizes_NoCrossContamination(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalKimiResponse(body.Model, "ok"))
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake, []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"})

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		for _, model := range []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"} {
			wg.Add(1)
			go func(model string) {
				defer wg.Done()
				s := newCaptureStream()
				if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
					Model:    model,
					Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
				}, s); err != nil {
					t.Errorf("%s: %v", model, err)
					return
				}
				if len(s.sent) != 1 || s.sent[0].Model != model {
					t.Errorf("%s: cross-contamination; got %#v", model, s.sent)
				}
			}(model)
		}
	}
	wg.Wait()
}

// 4.3-UNIT-016 (P0) — BR-1.9 large-prompt PII discipline (Kimi-specific
// concern per Architect risk profile R10): the 128k context size
// encourages long-document workloads; slog discipline regression here
// leaks large user content into logs at high cost. This test drives a
// 100K-character prompt and asserts none of it appears in the slog
// output.
func TestChat_NonStreaming_PIIsafeLogs_LargePromptKimi128k(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalKimiResponse("moonshot-v1-128k", "ok"))
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"moonshot-v1-128k"})

	// 100K characters of recognisable content.
	const marker = "KIMI-128K-LARGE-DOC-CANARY-XYZ"
	largePrompt := strings.Repeat(marker, 100000/len(marker)+1)
	stream := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "moonshot-v1-128k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: largePrompt}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if strings.Contains(buf.String(), marker) {
		t.Fatalf("BR-1.9 (Kimi 128k): slog leaked large prompt content")
	}
}

// 4.3-INT-006 (P0) — BR-4.5 context-length-exceeded slog disambiguation:
// upstream 400 with invalid_request_error + context-length body →
// connect.CodeUnavailable + slog upstream_error_kind=context_length_exceeded.
// Architect Round 1 m-1 ruling: body-aware classification is mandatory
// because the status code alone cannot distinguish.
func TestChat_NonStreaming_ContextLengthExceeded_BodyAwareKind(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"Your request exceeded the maximum context length of 8192 tokens."}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := newTestClient(fake.URL)
	svc := NewService(client, logger, []string{"moonshot-v1-8k"})

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "moonshot-v1-8k", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (context_length_exceeded)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
	if !strings.Contains(buf.String(), `"upstream_error_kind":"context_length_exceeded"`) {
		t.Fatalf("BR-4.5: slog missing upstream_error_kind=context_length_exceeded; logs: %s", buf.String())
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
	// EnableHTTP2 so ALPN can negotiate h2 when the client offers it;
	// stdlib http.Transport{ForceAttemptHTTP2:true} negotiates HTTP/2 over
	// TLS automatically.
	s.EnableHTTP2 = true
	s.StartTLS()
	return s
}

// newTestClient builds an upstream.Client whose transport accepts the
// httptest self-signed cert. Uses the stdlib http.Transport per OQ-4.2-5
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
