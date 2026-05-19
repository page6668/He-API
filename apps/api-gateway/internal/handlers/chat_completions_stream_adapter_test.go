// Story 4.1 Phase B — Tests for the adapter-dispatch branch of the
// streaming `/v1/chat/completions` handler (BR-2.1 dispatch + BR-2.5
// pre-flush boundary + BR-2.6 mid-stream-failure SSE error frame).
//
// Scenarios trace to docs/qa/assessments/4.1-test-design-20260519.md.
package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// canonicalStreamingChunks returns a happy-path 4-chunk adapter response:
// bootstrap role-only delta + 2 content deltas + 1 terminal-with-usage.
func canonicalStreamingChunks() []*adapterv1.ChatChunk {
	role := "assistant"
	hi := "Hi"
	there := " there"
	stop := "stop"
	return []*adapterv1.ChatChunk{
		{
			Id:      "chatcmpl-stream-001",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}, FinishReason: nil}},
		},
		{
			Id:      "chatcmpl-stream-001",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}, FinishReason: nil}},
		},
		{
			Id:      "chatcmpl-stream-001",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &there}, FinishReason: &stop}},
		},
		{
			Id:      "chatcmpl-stream-001",
			Object:  "chat.completion.chunk",
			Created: 1700000000,
			Model:   "deepseek-v3",
			Choices: []*adapterv1.Choice{},
			Usage:   &adapterv1.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
		},
	}
}

// 4.1-UNIT-030 (P0) — BR-2.1 streaming-dispatch hit. stream=true +
// model=deepseek-v3 → AdapterChunker forwards adapter chunks as SSE; the
// MockChunker path is bypassed.
func TestServeStream_AdapterDispatch_OnRegistryHit(t *testing.T) {
	fh := &fakeHandle{resp: &fakeStream{chunks: canonicalStreamingChunks()}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}],"stream":true}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/event-stream; charset=utf-8", got)
	}
	if !strings.HasSuffix(rr.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("body missing data: [DONE]\\n\\n suffix; body=%s", rr.Body.String())
	}
	// Adapter request had Stream=true.
	if fh.called != 1 || !fh.lastReq.Stream {
		t.Fatalf("adapter called %d times stream=%v; want 1 + stream=true", fh.called, fh.lastReq.Stream)
	}
}

// 4.1-UNIT-030b — registry MISS on streaming → fall through to MockChunker
// (Story-3.4 path preserved).
func TestServeStream_MockFallback_OnRegistryMiss(t *testing.T) {
	fh := &fakeHandle{resp: &fakeStream{chunks: canonicalStreamingChunks()}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"qwen-max","messages":[{"role":"user","content":"Hi"}],"stream":true}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if fh.called != 0 {
		t.Fatalf("adapter called %d times for qwen-max; want 0 (mock path)", fh.called)
	}
	if !strings.Contains(rr.Body.String(), "He-API mock") {
		t.Fatalf("expected mock-content SSE for qwen-max; got body:\n%s", rr.Body.String())
	}
}

// 4.1-UNIT-031 (P0) — BR-2.5 emit-before-flush: when the adapter Chat call
// itself errors (pre-stream), the gateway emits a JSON envelope NOT an SSE
// frame (status 502, Content-Type application/json).
func TestServeStream_PreFlushFailure_EmitsJSONEnvelope(t *testing.T) {
	fh := &fakeHandle{err: connect.NewError(connect.CodeUnavailable, errors.New("dial failed"))}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "502_upstream_unavailable" {
		t.Fatalf("error.code = %v, want 502_upstream_unavailable", errObj["code"])
	}
}

// 4.1-UNIT-031b — BR-2.5 boundary: pre-stream DeadlineExceeded → 504 JSON.
func TestServeStream_PreFlushDeadline_Emits504JSON(t *testing.T) {
	fh := &fakeHandle{err: connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout"))}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)

	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rr.Code)
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "504_upstream_timeout" {
		t.Fatalf("error.code = %v, want 504_upstream_timeout", errObj["code"])
	}
}

// 4.1-UNIT-032 (P0) — BR-2.6 mid-stream failure: stream emits 2 good chunks
// then surfaces an error. Gateway must NOT switch back to JSON (headers
// already flushed); it emits an SSE error frame + `data: [DONE]\n\n`.
func TestServeStream_PostFlushFailure_EmitsSSEErrorFrameThenDONE(t *testing.T) {
	role := "assistant"
	hi := "Hi"
	chunks := []*adapterv1.ChatChunk{
		{
			Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}, FinishReason: nil}},
		},
		{
			Id: "x", Object: "chat.completion.chunk", Created: 1, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &hi}, FinishReason: nil}},
		},
	}
	fh := &fakeHandle{resp: &fakeStream{
		chunks: chunks,
		err:    connect.NewError(connect.CodeUnavailable, errors.New("mid-stream RST")),
	}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)

	// Status remains 200 because headers were flushed before the failure.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (post-flush)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"502_upstream_unavailable"`) {
		t.Fatalf("SSE body missing inline error envelope:\n%s", rr.Body.String())
	}
	if !strings.HasSuffix(rr.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("SSE body missing trailing [DONE]; body:\n%s", rr.Body.String())
	}
	// Must have emitted the 2 content frames BEFORE the error frame.
	dataCount := strings.Count(rr.Body.String(), "\ndata: ") + 1
	// Expect: 2 content frames + 1 error frame + 1 [DONE] = 4 data lines.
	if dataCount < 4 {
		t.Fatalf("expected >= 4 data: lines, got %d in:\n%s", dataCount, rr.Body.String())
	}
}

// 4.1-UNIT-033 — happy-path tail-usage chunk preserved in the SSE stream
// (BR-3.7). Penultimate data: line carries the usage JSON.
func TestServeStream_TailUsage_ForwardedBeforeDone(t *testing.T) {
	fh := &fakeHandle{resp: &fakeStream{chunks: canonicalStreamingChunks()}}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Penultimate event must carry usage:{...}.
	body := rr.Body.String()
	if !strings.Contains(body, `"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`) {
		t.Fatalf("tail-usage chunk missing in SSE body:\n%s", body)
	}
	// usage must appear BEFORE [DONE].
	usageIdx := strings.Index(body, `"usage":`)
	doneIdx := strings.Index(body, "data: [DONE]")
	if usageIdx < 0 || doneIdx < 0 || usageIdx >= doneIdx {
		t.Fatalf("usage must precede [DONE]; usageIdx=%d doneIdx=%d", usageIdx, doneIdx)
	}
}

// Compile-time guard against helper drift.
var _ = httptest.NewRecorder
