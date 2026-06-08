// Story 6.4 AC2/AC4 — gateway parallel A/B dispatch + merge + dual-billing +
// scope + streaming guard (non-streaming happy/partial paths).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-022/023        merge: choices re-indexed + x_he_model; usage summed; model=legA
//	6.4-UNIT-025            one leg ∉ scope -> 403_model_not_in_scope whole request
//	6.4-BLIND-CONCURRENCY   both legs enter Chat() before either returns (-race)
//	6.4-INT-010             200 merged + X-He-AB-Models header; no X-He-Selected-Model
//	6.4-INT-011             dual-billing: TPMDeduct == legA.total + legB.total
//	6.4-UNIT-040            stream=true + A/B header -> 400 before dispatch
//	6.4-UNIT-043            no A/B header -> X-He-Selected-Model present (zero regression)
package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// legChunk builds a terminal ChatChunk attributed to a specific model with
// explicit usage so the merge + dual-billing assertions are deterministic.
func legChunk(id, model, content string, prompt, completion int) *adapterv1.ChatChunk {
	role := "assistant"
	c := content
	stop := "stop"
	return &adapterv1.ChatChunk{
		Id:      id,
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   model,
		Choices: []*adapterv1.Choice{{
			Index:        0,
			Delta:        &adapterv1.Delta{Role: &role, Content: &c},
			FinishReason: &stop,
		}},
		Usage:        &adapterv1.Usage{PromptTokens: int32(prompt), CompletionTokens: int32(completion), TotalTokens: int32(prompt + completion)},
		FinishReason: &stop,
	}
}

func legHandle(id, model, content string, prompt, completion int) *fakeHandle {
	return newSingleChunkHandle(legChunk(id, model, content, prompt, completion))
}

// abBarrier proves PARALLEL dispatch (Q-C): each leg signals arrival, then
// blocks until ALL legs have arrived. A sequential dispatch never reaches the
// arrival count, times out, and surfaces an error — failing the 200 assertion.
type abBarrier struct {
	mu    sync.Mutex
	count int
	want  int
	ready chan struct{}
}

func newABBarrier(want int) *abBarrier { return &abBarrier{want: want, ready: make(chan struct{})} }

func (b *abBarrier) arrive() bool {
	b.mu.Lock()
	b.count++
	if b.count == b.want {
		close(b.ready)
	}
	b.mu.Unlock()
	select {
	case <-b.ready:
		return true
	case <-time.After(2 * time.Second):
		return false // barrier never met -> legs were not concurrent
	}
}

type barrierHandle struct {
	chunk  *adapterv1.ChatChunk
	gate   *abBarrier
	called int
}

func (h *barrierHandle) Chat(_ context.Context, req *adapterv1.ChatRequest, _ http.Header) (adapterclient.Stream, error) {
	h.called++
	if !h.gate.arrive() {
		return nil, errFakeUpstream
	}
	return &fakeStream{chunks: []*adapterv1.ChatChunk{h.chunk}}, nil
}

// abHandler wires a passthrough-A/B router (nil routing client -> the gateway
// dispatches the parsed legs directly) + the adapter registry + deducter.
func abHandler(t *testing.T, reg *adapterclient.Registry, dd handlers.TokenDeducter, opts ...handlers.ChatHandlerOption) *handlers.ChatCompletionsHandler {
	t.Helper()
	base := []handlers.ChatHandlerOption{
		handlers.WithRouter(routingclient.NewDecider(nil, nil)), // nil client -> passthrough A/B
		handlers.WithAdapterRegistry(reg),
	}
	if dd != nil {
		base = append(base, handlers.WithTokenDeducter(dd))
	}
	base = append(base, opts...)
	return handlers.NewChatCompletionsHandler(nil, base...)
}

// doABRequest invokes the handler with an X-He-AB-Models header (+ optional
// scope claims). stream toggles the request body's stream flag.
func doABRequest(t *testing.T, h *handlers.ChatCompletionsHandler, abHeader string, stream bool, scopeModels []string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]`
	if stream {
		body += `,"stream":true`
	}
	body += `}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer he-test-key-stub")
	if abHeader != "" {
		req.Header.Set(routingclient.ABModelsHeader, abHeader)
	}
	ctx := withBearerCtx(req.Context())
	if scopeModels != nil {
		ctx = middleware.WithCacheValue(ctx, &middleware.CachedClaims{ScopeModels: scopeModels})
	}
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

type abBody struct {
	Model   string `json:"model"`
	Object  string `json:"object"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
		XHeModel     string `json:"x_he_model"`
		XHeError     *struct {
			Code string `json:"code"`
		} `json:"x_he_error"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// 6.4-INT-010 + 6.4-UNIT-022/023 (P0) — both legs succeed: merged body carries
// both choices re-indexed with x_he_model; usage summed; model=legA; the
// X-He-AB-Models response header lists served ids; NO X-He-Selected-Model.
func TestAB_BothSucceed_Merge(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A-answer", 10, 20)
	legB := legHandle("cmpl-b", "deepseek-v3", "B-answer", 30, 40)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := abHandler(t, reg, dd)

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var b abBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if b.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", b.Object)
	}
	if b.Model != "qwen-max" {
		t.Errorf("top-level model = %q, want qwen-max (leg A)", b.Model)
	}
	if len(b.Choices) != 2 {
		t.Fatalf("len(choices) = %d, want 2", len(b.Choices))
	}
	if b.Choices[0].Index != 0 || b.Choices[0].XHeModel != "qwen-max" || b.Choices[0].Message.Content != "A-answer" {
		t.Errorf("choices[0] = %+v, want {index:0, x_he_model:qwen-max, A-answer}", b.Choices[0])
	}
	if b.Choices[1].Index != 1 || b.Choices[1].XHeModel != "deepseek-v3" || b.Choices[1].Message.Content != "B-answer" {
		t.Errorf("choices[1] = %+v, want {index:1, x_he_model:deepseek-v3, B-answer}", b.Choices[1])
	}
	// usage summed: (10+20)+(30+40) = 100
	if b.Usage.TotalTokens != 100 || b.Usage.PromptTokens != 40 || b.Usage.CompletionTokens != 60 {
		t.Errorf("usage = %+v, want {prompt:40, completion:60, total:100}", b.Usage)
	}
	// X-He-AB-Models response header = served ids in order; NO X-He-Selected-Model (BR2-7).
	if got := rr.Header().Get(routingclient.ABModelsHeader); got != "qwen-max,deepseek-v3" {
		t.Errorf("X-He-AB-Models header = %q, want qwen-max,deepseek-v3", got)
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "" {
		t.Errorf("X-He-Selected-Model = %q, want empty on A/B path (BR2-7)", got)
	}
	// 6.4-INT-011 dual-billing INVARIANT (Q-F): ONE TPMDeduct of the summed total.
	if dd.calls != 1 || dd.lastTokens != 100 {
		t.Errorf("TPMDeduct calls=%d lastTokens=%d, want 1 call of 100 (legA.total+legB.total)", dd.calls, dd.lastTokens)
	}
}

// 6.4-BLIND-CONCURRENCY-001/002 (P1) — both legs enter Chat() before either
// returns (barrier proves PARALLEL dispatch — Q-C). Run with -race.
func TestAB_ParallelDispatch_Barrier(t *testing.T) {
	gate := newABBarrier(2)
	legA := &barrierHandle{chunk: legChunk("cmpl-a", "qwen-max", "A", 1, 1), gate: gate}
	legB := &barrierHandle{chunk: legChunk("cmpl-b", "deepseek-v3", "B", 1, 1), gate: gate}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	h := abHandler(t, reg, &countingDeducter{})

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (barrier met -> legs were concurrent); body=%s", rr.Code, rr.Body.String())
	}
	if legA.called != 1 || legB.called != 1 {
		t.Errorf("calls A=%d B=%d, want 1/1", legA.called, legB.called)
	}
}

// 6.4-UNIT-025 (P0) — one leg outside the key scope.models -> 403 for the WHOLE
// request, BEFORE any dispatch (Q-K).
func TestAB_LegOutOfScope_403(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A", 1, 1)
	legB := legHandle("cmpl-b", "deepseek-v3", "B", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	h := abHandler(t, reg, &countingDeducter{})

	// scope allows only qwen-max -> deepseek-v3 leg is out of scope.
	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, []string{"qwen-max"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if legA.called != 0 || legB.called != 0 {
		t.Errorf("legs dispatched (A=%d B=%d), want 0/0 (403 before dispatch)", legA.called, legB.called)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "403_model_not_in_scope" {
		t.Errorf("error.code = %q, want 403_model_not_in_scope", env.Error.Code)
	}
}

// 6.4-UNIT-040 (P0) — stream=true + X-He-AB-Models -> 400 before any dispatch.
func TestAB_Streaming_400(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": legA, "deepseek-v3": legA})
	h := abHandler(t, reg, &countingDeducter{})

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", true, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (streaming A/B guard)", rr.Code)
	}
	if legA.called != 0 {
		t.Errorf("dispatched %d, want 0 (guard fires before dispatch)", legA.called)
	}
}

// 6.4-UNIT-005/006 parse failures at the handler -> 400.
func TestAB_BadHeaderCount_400(t *testing.T) {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": legHandle("c", "qwen-max", "x", 1, 1)})
	h := abHandler(t, reg, &countingDeducter{})
	for _, hdr := range []string{"qwen-max", "a,b,c", "qwen-max,qwen-max"} {
		rr := doABRequest(t, h, hdr, false, nil)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("header %q: status = %d, want 400", hdr, rr.Code)
		}
	}
}

// 6.4-UNIT-043 (P0) — a request WITHOUT the A/B header is unchanged: the
// single-model adapter path runs, X-He-Selected-Model present, no A/B header.
func TestAB_NoHeader_ZeroRegression(t *testing.T) {
	fh := legHandle("c", "qwen-max", "x", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": fh})
	h := abHandler(t, reg, &countingDeducter{})

	rr := doABRequest(t, h, "", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "qwen-max" {
		t.Errorf("X-He-Selected-Model = %q, want qwen-max (non-A/B path)", got)
	}
	if got := rr.Header().Get(routingclient.ABModelsHeader); got != "" {
		t.Errorf("X-He-AB-Models response header = %q, want empty on non-A/B path", got)
	}
}
