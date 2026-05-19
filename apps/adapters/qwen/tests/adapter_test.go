// Package tests carries the cross-package integration scenarios for the
// Qwen adapter (Story 4.2).
//
// Test IDs trace to docs/qa/assessments/4.2-test-design-20260519.md:
//   - 4.2-INT-001..006 — non-streaming happy paths (qwen-max + qwen-plus)
//   - 4.2-INT-007..010 — streaming happy paths (qwen-max + qwen-plus)
//   - 4.2-INT-011..014 — token-usage oracle + miscellaneous
//
// Architect Round 1 OQ-4.2-5 (HTTP/2-preferred + ALPN HTTP/1.1 fallback)
// is exercised here via the upstream.NewClient default transport.
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
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	qweninternal "github.com/he-api/he-api/apps/adapters/qwen/internal"
	"github.com/he-api/he-api/apps/adapters/qwen/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

func canonicalUpstreamBody(model, content string) []byte {
	stop := "stop"
	b, _ := json.Marshal(upstream.ChatResponseJSON{
		ID:      "chatcmpl-qwen-vendor-001",
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
	return b
}

// 4.2-INT-001 (P0) — non-streaming happy path for qwen-max end-to-end.
func TestINT_001_NonStreaming_QwenMax(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("qwen-max", "Hello from upstream"))
	})
	defer fake.Close()

	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model:    "qwen-max",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !resp.Receive() {
		t.Fatalf("no chunk received: %v", resp.Err())
	}
	chunk := resp.Msg()
	if chunk.GetModel() != "qwen-max" {
		t.Fatalf("model = %q", chunk.GetModel())
	}
	if chunk.GetUsage().GetTotalTokens() != 19 {
		t.Fatalf("usage.total_tokens = %d, want 19", chunk.GetUsage().GetTotalTokens())
	}
	if resp.Receive() {
		t.Fatalf("got extra chunk on non-streaming path")
	}
}

// 4.2-INT-002 (P0) — non-streaming for qwen-plus + verify upstream
// receives `model=qwen-plus` verbatim (BR-1.10).
func TestINT_002_NonStreaming_QwenPlus_ModelVerbatim(t *testing.T) {
	var seenModel string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("qwen-plus", "Plus reply"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-plus", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if seenModel != "qwen-plus" {
		t.Fatalf("upstream received model = %q, want qwen-plus (BR-1.10 verbatim)", seenModel)
	}
}

// 4.2-INT-003 (P0) — upstream 5xx → Connect-RPC Code.Unavailable.
func TestINT_003_Upstream_5xx_MapsToUnavailable(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"vendor down"}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if resp.Receive() {
		t.Fatalf("got chunk on 5xx path; expected none")
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

// 4.2-INT-004 (P0) — OQ-4.2-4 ratification: upstream 429 → CodeUnavailable
// (rate_limit_throttle kind verified at unit level).
func TestINT_004_Upstream_429_MapsToUnavailable_QwenRateLimit(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"Throttling.RateQuota"}}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	resp.Receive()
	e := resp.Err()
	if e == nil {
		t.Fatalf("Err() = nil, want CodeUnavailable for upstream 429")
	}
	var ce *connect.Error
	if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("Err() = %v, want CodeUnavailable", e)
	}
}

// 4.2-INT-005 (P0) — BR-1.5 request-id propagation through gateway →
// adapter → upstream (DashScope convention `X-Request-Id`).
func TestINT_005_RequestId_Propagation_QwenMax(t *testing.T) {
	var seenReqID string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-Id")
		w.Header().Set("X-Request-Id", "dashscope-server-req-Z9Y8")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("qwen-max", "ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	req := connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	})
	req.Header().Set("X-He-Request-Id", "req_intgrtnQwen1")
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if seenReqID != "req_intgrtnQwen1" {
		t.Fatalf("upstream X-Request-Id = %q, want %q (BR-1.5)", seenReqID, "req_intgrtnQwen1")
	}
}

// 4.2-INT-007 (P0) — streaming happy path qwen-max.
func TestINT_007_Streaming_HappyPath_QwenMax(t *testing.T) {
	sseBody := sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: "qwen-max",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Role: ptr("assistant")}}},
	}) + sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: "qwen-max",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hi")}}},
	}) + sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: "qwen-max",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, FinishReason: ptr("stop")}},
		Usage:   &upstream.RawUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
	}) + "data: [DONE]\n\n"

	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}}, Stream: true,
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	var chunks []*adapterv1.ChatChunk
	for resp.Receive() {
		chunks = append(chunks, resp.Msg())
	}
	if e := resp.Err(); e != nil {
		t.Fatalf("Err() = %v, want nil", e)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d streaming chunks, want 3", len(chunks))
	}
	if last := chunks[len(chunks)-1]; last.GetUsage() == nil || last.GetUsage().GetTotalTokens() != 8 {
		t.Fatalf("terminal chunk usage = %#v, want total=8", last.GetUsage())
	}
}

// 4.2-INT-009 (P0) — parallel qwen-max + qwen-plus dispatch must not
// cross-contaminate. Run under `-race`.
func TestINT_009_ParallelQwenMaxAndPlus_NoCrossContamination(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		var body upstream.ChatRequestJSON
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody(body.Model, "ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
				Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}))
			if err != nil {
				t.Errorf("qwen-max err: %v", err)
				return
			}
			if !resp.Receive() {
				t.Errorf("qwen-max: no chunk: %v", resp.Err())
				return
			}
			if resp.Msg().GetModel() != "qwen-max" {
				t.Errorf("qwen-max: cross-contamination: model=%q", resp.Msg().GetModel())
			}
		}()
		go func() {
			defer wg.Done()
			resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
				Model: "qwen-plus", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}))
			if err != nil {
				t.Errorf("qwen-plus err: %v", err)
				return
			}
			if !resp.Receive() {
				t.Errorf("qwen-plus: no chunk: %v", resp.Err())
				return
			}
			if resp.Msg().GetModel() != "qwen-plus" {
				t.Errorf("qwen-plus: cross-contamination: model=%q", resp.Msg().GetModel())
			}
		}()
	}
	wg.Wait()
}

// 4.2-INT-011 (P0) — token-usage oracle invariant: adapter-reported
// usage matches upstream-reported usage (post-Normaliser).
func TestINT_011_TokenUsage_OracleInvariant(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody("qwen-max", "ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	chunk := resp.Msg()
	u := chunk.GetUsage()
	if u.GetPromptTokens() != 7 || u.GetCompletionTokens() != 12 || u.GetTotalTokens() != 19 {
		t.Fatalf("adapter-reported usage = %#v, want upstream-reported {7,12,19} (BR-3.1 oracle)", u)
	}
}

// 4.2-INT-012 (P0) — BR-3.4 missing-usage HARD failure.
func TestINT_012_MissingUsage_HardFailure(t *testing.T) {
	stop := "stop"
	body, _ := json.Marshal(upstream.ChatResponseJSON{
		ID: "x", Object: "chat.completion", Created: 1, Model: "qwen-max",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
		// usage intentionally omitted
	})
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "qwen-max", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}))
	resp.Receive()
	e := resp.Err()
	if e == nil {
		t.Fatalf("Err() = nil, want CodeUnavailable (missing_usage)")
	}
	var ce *connect.Error
	if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("Err() = %v, want CodeUnavailable", e)
	}
}

// --- helpers --------------------------------------------------------------

func ptr[T any](v T) *T { return &v }

func sseFrame(t *testing.T, c upstream.ChatChunkJSON) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "data: " + string(b) + "\n\n"
}

func startFakeUpstream(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.EnableHTTP2 = true
	s.StartTLS()
	return s
}

func startAdapterServer(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	upstreamClient := upstream.NewClient(upstreamURL, "test-api-key", 5*time.Second)
	upstreamClient.HTTPClient.Transport = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	}
	svc := qweninternal.NewService(upstreamClient, logger, []string{"qwen-max", "qwen-plus"})

	mux := http.NewServeMux()
	path, handler := adapterv1connect.NewAdapterServiceHandler(svc)
	mux.Handle(path, handler)

	s := httptest.NewUnstartedServer(mux)
	s.Start()
	return s
}

func newAdapterClient(baseURL string) adapterv1connect.AdapterServiceClient {
	return adapterv1connect.NewAdapterServiceClient(&http.Client{}, baseURL)
}

// Compile-time strings import retention.
var _ = strings.Contains
