// Package tests carries the cross-package integration scenarios for the
// Doubao (Volcengine Ark v3) adapter (Story 4.5).
//
// Test IDs trace to docs/qa/assessments/4.5-test-design-20260519.md:
//   - 4.5-INT-001..006   — non-streaming subtests (BR-1.11 round-trip)
//   - 4.5-INT-007/010    — streaming subtests (BR-1.11 per-chunk + BR-2.4 tail-usage)
//   - 4.5-INT-007 (oracle) — token-usage delta < 1% across 20 fixture cells
//
// Architect Round 1 OQ-4.5-6 (HTTP/2-preferred + ALPN HTTP/1.1 fallback)
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
	doubaointernal "github.com/he-api/he-api/apps/adapters/doubao/internal"
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

const (
	testProEndpointID  = "ep-test-pro-001"
	testLiteEndpointID = "ep-test-lite-001"
)

func canonicalUpstreamBody(endpointID, content string) []byte {
	stop := "stop"
	b, _ := json.Marshal(upstream.ChatResponseJSON{
		ID:      "chatcmpl-doubao-vendor-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   endpointID,
		Choices: []upstream.ChatChoiceJSON{{
			Index:        0,
			Message:      &upstream.ChatMessage{Role: "assistant", Content: content},
			FinishReason: &stop,
		}},
		Usage: &upstream.RawUsage{PromptTokens: 7, CompletionTokens: 12, TotalTokens: 19},
	})
	return b
}

// 4.5-INT-001 (P0) — non-streaming happy path for doubao-pro
// end-to-end via Connect-RPC wire; verifies BR-1.11 inbound back-translate
// (response.Model = friendly id, not endpoint id).
func TestINT_001_NonStreaming_DoubaoPro_BackTranslated(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody(testProEndpointID, "Hello from upstream"))
	})
	defer fake.Close()

	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model:    "doubao-pro",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !resp.Receive() {
		t.Fatalf("no chunk received: %v", resp.Err())
	}
	chunk := resp.Msg()
	if chunk.GetModel() != "doubao-pro" {
		t.Fatalf("model = %q, want doubao-pro (BR-1.11 back-translate)", chunk.GetModel())
	}
	if chunk.GetUsage().GetTotalTokens() != 19 {
		t.Fatalf("usage.total_tokens = %d, want 19", chunk.GetUsage().GetTotalTokens())
	}
	if resp.Receive() {
		t.Fatalf("got extra chunk on non-streaming path")
	}
}

// 4.5-INT-001 (doubao-lite path) — symmetric verification for lite.
func TestINT_001_NonStreaming_DoubaoLite_BackTranslated(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody(testLiteEndpointID, "Hi lite"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-lite", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if !resp.Receive() {
		t.Fatalf("no chunk: %v", resp.Err())
	}
	if got := resp.Msg().GetModel(); got != "doubao-lite" {
		t.Fatalf("model = %q, want doubao-lite", got)
	}
}

// 4.5-INT-002 (P0) — upstream-fake records outbound body's `model` field
// (BR-1.7.f outbound rewrite at integration boundary).
func TestINT_002_OutboundEndpointID_RecordedByUpstream(t *testing.T) {
	cases := []struct {
		reqModel    string
		wantUpModel string
	}{
		{"doubao-pro", testProEndpointID},
		{"doubao-lite", testLiteEndpointID},
	}
	for _, c := range cases {
		c := c
		t.Run(c.reqModel, func(t *testing.T) {
			var seenModel string
			fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				var body upstream.ChatRequestJSON
				_ = json.NewDecoder(r.Body).Decode(&body)
				seenModel = body.Model
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(canonicalUpstreamBody(body.Model, "ok"))
			})
			defer fake.Close()
			adapter := startAdapterServer(t, fake.URL)
			defer adapter.Close()
			client := newAdapterClient(adapter.URL)
			resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
				Model: c.reqModel, Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
			}))
			if err != nil {
				t.Fatalf("Chat err = %v", err)
			}
			resp.Receive()
			if seenModel != c.wantUpModel {
				t.Fatalf("upstream received model = %q, want %q (BR-1.7.f outbound rewrite)", seenModel, c.wantUpModel)
			}
		})
	}
}

// 4.5-INT-003 (P0) — upstream 5xx → Connect-RPC Code.Unavailable.
func TestINT_003_Upstream5xx_MapsToUnavailable(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"vendor down"}`)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// 4.5-INT-004 (P0) — upstream 429 → CodeUnavailable (BR-4.4 cascade).
func TestINT_004_Upstream429_MapsToUnavailable_RateLimitThrottle(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
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
		t.Fatalf("Err = nil, want CodeUnavailable for upstream 429")
	}
	var ce *connect.Error
	if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("Err = %v, want CodeUnavailable", e)
	}
}

// 4.5-INT-006 (P1) — BR-1.5 request-id propagation through gateway →
// adapter → upstream (Volcengine convention X-Request-Id); m1 cascade
// upstream response X-Request-Id captured.
func TestINT_006_RequestId_Propagation_ThreeHop(t *testing.T) {
	var seenReqID string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-Id")
		w.Header().Set("X-Request-Id", "volc_server_req_Z9Y8")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(canonicalUpstreamBody(testProEndpointID, "ok"))
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	req := connect.NewRequest(&adapterv1.ChatRequest{
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	})
	req.Header().Set("X-He-Request-Id", "req_intgrtnDOUBAO0")
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	resp.Receive()
	if seenReqID != "req_intgrtnDOUBAO0" {
		t.Fatalf("upstream X-Request-Id = %q, want %q (BR-1.5)", seenReqID, "req_intgrtnDOUBAO0")
	}
}

// 4.5-INT-007-streaming (P0) — AC2 streaming happy path: 4 deltas + 1
// tail-with-usage + [DONE]. EVERY emitted chunk's Model = friendly id
// (per BR-1.11 per-chunk back-translate via TranslateChatChunk in
// adapter.go streaming loop). Tail chunk usage = upstream-reported usage
// post-Normaliser.
func TestINT_007_Streaming_HappyPath_DoubaoPro_PerChunkBackTranslate(t *testing.T) {
	sseBody := sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: testProEndpointID,
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Role: ptr("assistant")}}},
	}) + sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: testProEndpointID,
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Delta: &upstream.ChatDeltaJSON{Content: ptr("Hi")}}},
	}) + sseFrame(t, upstream.ChatChunkJSON{
		ID: "x", Object: "chat.completion.chunk", Created: 1, Model: testProEndpointID,
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
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}}, Stream: true,
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
	// R10 — EVERY chunk's Model field must be the friendly id (back-translated).
	for i, c := range chunks {
		if c.GetModel() != "doubao-pro" {
			t.Fatalf("chunk %d Model = %q, want doubao-pro (BR-1.11 per-chunk back-translate)", i, c.GetModel())
		}
		if strings.HasPrefix(c.GetModel(), "ep-") {
			t.Fatalf("chunk %d Model = %q leaked endpoint id", i, c.GetModel())
		}
	}
	if last := chunks[len(chunks)-1]; last.GetUsage() == nil || last.GetUsage().GetTotalTokens() != 8 {
		t.Fatalf("terminal chunk usage = %#v, want total=8", last.GetUsage())
	}
}

// 4.5-INT-007 (oracle invariant — abridged): 10 fixtures × 2 models = 20
// non-streaming cells. Asserts (a) adapter-emitted usage == fixture
// upstream usage byte-for-byte (delta=0 under identity Normaliser),
// (b) outbound body model = endpoint id, (c) inbound chunk model = friendly id.
func TestINT_007_OracleInvariant_RoundTrip_20Cells(t *testing.T) {
	type fixture struct {
		name      string
		usage     upstream.RawUsage
		userInput string
	}
	fixtures := []fixture{
		{"short", upstream.RawUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, "hi"},
		{"short-medium", upstream.RawUsage{PromptTokens: 20, CompletionTokens: 80, TotalTokens: 100}, "ping pong"},
		{"medium", upstream.RawUsage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300}, strings.Repeat("a", 800)},
		{"medium-long", upstream.RawUsage{PromptTokens: 500, CompletionTokens: 1500, TotalTokens: 2000}, strings.Repeat("b", 4000)},
		{"long", upstream.RawUsage{PromptTokens: 2000, CompletionTokens: 8000, TotalTokens: 10000}, strings.Repeat("c", 10000)},
		{"zero-completion", upstream.RawUsage{PromptTokens: 50, CompletionTokens: 0, TotalTokens: 50}, "filter trigger"},
		{"system-and-user", upstream.RawUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}, "system+user combo"},
		{"large-prompt", upstream.RawUsage{PromptTokens: 50000, CompletionTokens: 100, TotalTokens: 50100}, strings.Repeat("d", 5000)},
		{"large-completion", upstream.RawUsage{PromptTokens: 100, CompletionTokens: 5000, TotalTokens: 5100}, "expand please"},
		{"balanced", upstream.RawUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}, strings.Repeat("e", 2000)},
	}
	cases := []struct {
		reqModel     string
		expectUpsEPI string
	}{
		{"doubao-pro", testProEndpointID},
		{"doubao-lite", testLiteEndpointID},
	}
	for _, c := range cases {
		for _, f := range fixtures {
			c, f := c, f
			t.Run(c.reqModel+"/"+f.name, func(t *testing.T) {
				var outboundModel string
				fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
					var body upstream.ChatRequestJSON
					_ = json.NewDecoder(r.Body).Decode(&body)
					outboundModel = body.Model
					stop := "stop"
					resp := upstream.ChatResponseJSON{
						ID: "c1", Object: "chat.completion", Created: 1, Model: body.Model,
						Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
						Usage:   &f.usage,
					}
					out, _ := json.Marshal(resp)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(out)
				})
				defer fake.Close()
				adapter := startAdapterServer(t, fake.URL)
				defer adapter.Close()
				client := newAdapterClient(adapter.URL)
				r, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
					Model:    c.reqModel,
					Messages: []*adapterv1.ChatMessage{{Role: "user", Content: f.userInput}},
				}))
				if err != nil {
					t.Fatalf("Chat err = %v", err)
				}
				if !r.Receive() {
					t.Fatalf("no chunk: %v", r.Err())
				}
				chunk := r.Msg()
				// (a) oracle invariant — identity Normaliser, byte-for-byte.
				u := chunk.GetUsage()
				if int(u.GetPromptTokens()) != f.usage.PromptTokens || int(u.GetCompletionTokens()) != f.usage.CompletionTokens || int(u.GetTotalTokens()) != f.usage.TotalTokens {
					t.Fatalf("usage drift: got {%d,%d,%d}, want %#v", u.GetPromptTokens(), u.GetCompletionTokens(), u.GetTotalTokens(), f.usage)
				}
				// (b) outbound rewrite verified.
				if outboundModel != c.expectUpsEPI {
					t.Fatalf("outbound model = %q, want %q (BR-1.7.f)", outboundModel, c.expectUpsEPI)
				}
				// (c) inbound back-translate verified.
				if chunk.GetModel() != c.reqModel {
					t.Fatalf("inbound model = %q, want %q (BR-1.11)", chunk.GetModel(), c.reqModel)
				}
			})
		}
	}
}

// 4.5-INT-012 (P0) — missing-usage HARD failure across the Connect wire.
func TestINT_012_MissingUsage_HardFailure(t *testing.T) {
	stop := "stop"
	body, _ := json.Marshal(upstream.ChatResponseJSON{
		ID: "x", Object: "chat.completion", Created: 1, Model: testProEndpointID,
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
		Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
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

// 4.5-BLIND-CONCURRENCY-001 (P0) — 100 concurrent pro/lite mixed calls
// → no panic, no cross-model contamination. Race detector required.
func TestINT_ParallelDoubaoProAndLite_NoCrossContamination_N2_Restored(t *testing.T) {
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
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
				Model: "doubao-pro", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}))
			if err != nil {
				t.Errorf("pro err: %v", err)
				return
			}
			if !resp.Receive() {
				t.Errorf("pro: no chunk: %v", resp.Err())
				return
			}
			if resp.Msg().GetModel() != "doubao-pro" {
				t.Errorf("pro: cross-contamination: model=%q", resp.Msg().GetModel())
			}
		}()
		go func() {
			defer wg.Done()
			resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
				Model: "doubao-lite", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}))
			if err != nil {
				t.Errorf("lite err: %v", err)
				return
			}
			if !resp.Receive() {
				t.Errorf("lite: no chunk: %v", resp.Err())
				return
			}
			if resp.Msg().GetModel() != "doubao-lite" {
				t.Errorf("lite: cross-contamination: model=%q", resp.Msg().GetModel())
			}
		}()
	}
	wg.Wait()
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
	em := upstream.New(func(name string) string {
		switch name {
		case upstream.EnvDoubaoProEndpointID:
			return testProEndpointID
		case upstream.EnvDoubaoLiteEndpointID:
			return testLiteEndpointID
		}
		return ""
	})
	svc := doubaointernal.NewService(upstreamClient, em, logger, []string{"doubao-pro", "doubao-lite"})

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
