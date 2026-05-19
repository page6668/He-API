// Story 4.1 — Tests for the adapter-dispatch branch in
// ChatCompletionsHandler.ServeHTTP (non-streaming path). The streaming
// branch lands in Phase B.
//
// Scenario IDs trace to docs/qa/assessments/4.1-test-design-20260519.md.
package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// reqIDCtx stamps the request-id onto ctx via the shared accessor — used
// to simulate the request-id middleware having already run.
func reqIDCtx(ctx context.Context, id string) context.Context {
	return requestid.WithRequestID(ctx, id)
}

// fakeStream emits a fixed sequence of ChatChunks then Err()=nil at EOS.
type fakeStream struct {
	chunks []*adapterv1.ChatChunk
	idx    int
	closed bool
	err    error
}

func (s *fakeStream) Receive() bool {
	if s.idx >= len(s.chunks) {
		return false
	}
	s.idx++
	return true
}
func (s *fakeStream) Msg() *adapterv1.ChatChunk { return s.chunks[s.idx-1] }
func (s *fakeStream) Err() error                { return s.err }
func (s *fakeStream) Close() error              { s.closed = true; return nil }

// fakeHandle records Chat invocations and returns a configured response /
// error.
type fakeHandle struct {
	called      int
	lastReq     *adapterv1.ChatRequest
	lastHeaders http.Header
	resp        adapterclient.Stream
	err         error
}

func (h *fakeHandle) Chat(ctx context.Context, req *adapterv1.ChatRequest, headers http.Header) (adapterclient.Stream, error) {
	h.called++
	h.lastReq = req
	h.lastHeaders = headers
	if h.err != nil {
		return nil, h.err
	}
	return h.resp, nil
}

// newSingleChunkHandle wraps a single ChatChunk as the adapter's terminal-
// chunk response (Phase A non-streaming).
func newSingleChunkHandle(chunk *adapterv1.ChatChunk) *fakeHandle {
	return &fakeHandle{resp: &fakeStream{chunks: []*adapterv1.ChatChunk{chunk}}}
}

// canonicalAdapterChunk returns the terminal ChatChunk shape the DeepSeek
// adapter would emit on a happy-path /v1/chat/completions call with
// model=deepseek-v3.
func canonicalAdapterChunk() *adapterv1.ChatChunk {
	role := "assistant"
	content := "Hello from DeepSeek adapter."
	stop := "stop"
	return &adapterv1.ChatChunk{
		Id:      "chatcmpl-real-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "deepseek-v3",
		Choices: []*adapterv1.Choice{{
			Index:        0,
			Delta:        &adapterv1.Delta{Role: &role, Content: &content},
			FinishReason: &stop,
		}},
		Usage:        &adapterv1.Usage{PromptTokens: 5, CompletionTokens: 8, TotalTokens: 13},
		FinishReason: &stop,
	}
}

// 4.1-UNIT-013 (P0) — BR-1.2 adapter dispatch on Resolve-hit. Model
// "deepseek-v3" dispatches to the registered handle; mock-write block is
// bypassed.
func TestServeNonStream_AdapterDispatch_OnRegistryHit(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if fh.called != 1 {
		t.Fatalf("adapter handle called %d times, want 1", fh.called)
	}
	body := decodeBody(t, rr)
	if got, want := body["model"], "deepseek-v3"; got != want {
		t.Fatalf("body.model = %v, want %v", got, want)
	}
	choices, _ := body["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1; body=%v", len(choices), body)
	}
	choice0 := choices[0].(map[string]any)
	msg, _ := choice0["message"].(map[string]any)
	if msg["content"] != "Hello from DeepSeek adapter." {
		t.Fatalf("choices[0].message.content = %v, want adapter content", msg["content"])
	}
	usage, _ := body["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 5 {
		t.Fatalf("usage.prompt_tokens = %v, want 5", usage["prompt_tokens"])
	}
	// BR-1.6: X-He-Selected-Model present on success.
	if got, want := rr.Header().Get("X-He-Selected-Model"), "deepseek-v3"; got != want {
		t.Fatalf("X-He-Selected-Model = %q, want %q", got, want)
	}
}

// 4.1-UNIT-013 negative — registry miss falls through to mock path
// (Story-3.3 mock content). Adapter handle is NOT invoked.
func TestServeNonStream_MockFallback_OnRegistryMiss(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequest(t, h, `{"model":"qwen-max","messages":[{"role":"user","content":"Hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if fh.called != 0 {
		t.Fatalf("adapter handle called %d times, want 0 (qwen-max is not registered)", fh.called)
	}
	body := decodeBody(t, rr)
	choices := body["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if !strings.Contains(msg["content"].(string), "He-API mock") {
		t.Fatalf("expected mock content for qwen-max; got %v", msg["content"])
	}
}

// 4.1-UNIT-014 (P0) — BR-1.4 mapping. Adapter Connect-RPC `Code.Unavailable`
// → HTTP 502 + envelope `502_upstream_unavailable`.
func TestServeNonStream_AdapterUnavailable_Maps502(t *testing.T) {
	fh := &fakeHandle{err: connect.NewError(connect.CodeUnavailable, errors.New("upstream down"))}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}]}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "502_upstream_unavailable" {
		t.Fatalf("error.code = %v, want 502_upstream_unavailable", errObj["code"])
	}
	if errObj["type"] != "server_error" {
		t.Fatalf("error.type = %v, want server_error", errObj["type"])
	}
}

// 4.1-UNIT-015 (P0) — BR-1.4 mapping. `Code.DeadlineExceeded` → HTTP 504 +
// envelope `504_upstream_timeout`.
func TestServeNonStream_AdapterDeadlineExceeded_Maps504(t *testing.T) {
	fh := &fakeHandle{err: connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout"))}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}]}`)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rr.Code)
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "504_upstream_timeout" {
		t.Fatalf("error.code = %v, want 504_upstream_timeout", errObj["code"])
	}
}

// 4.1-UNIT-016 (P0) — BR-1.4 mapping. Adapter handle Chat() returns a
// non-connect error (e.g., dial failure → connection refused) → 502 +
// `502_upstream_unavailable` (we treat unreachable adapter as upstream-
// unavailable per BR-1.4).
func TestServeNonStream_AdapterDialFailure_Maps502(t *testing.T) {
	fh := &fakeHandle{err: errors.New("dial tcp 127.0.0.1:8080: connect: connection refused")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	rr := doRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}]}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "502_upstream_unavailable" {
		t.Fatalf("error.code = %v, want 502_upstream_unavailable", errObj["code"])
	}
}

// BR-1.5 — gateway propagates X-He-Request-Id as a Connect-RPC header on
// the outbound adapter call. The fake handle captures headers; assert
// the propagated value matches the response header.
func TestServeNonStream_PropagatesRequestId_AsConnectHeader(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`))
	req.Header.Set("Authorization", "Bearer test")
	// Simulate the requestid middleware having stamped the header.
	const fakeReqID = "req_aaaaaaaaaaaa"
	req = req.WithContext(reqIDCtx(req.Context(), fakeReqID))
	req = req.WithContext(withBearerCtx(req.Context()))
	rr := httptest.NewRecorder()
	rr.Header().Set("X-He-Request-Id", fakeReqID)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := fh.lastHeaders.Get("X-He-Request-Id"); got != fakeReqID {
		t.Fatalf("adapter inbound X-He-Request-Id = %q, want %q", got, fakeReqID)
	}
	// Also assert the proto field is populated.
	if fh.lastReq.HeRequestId != fakeReqID {
		t.Fatalf("ChatRequest.he_request_id = %q, want %q", fh.lastReq.HeRequestId, fakeReqID)
	}
}
