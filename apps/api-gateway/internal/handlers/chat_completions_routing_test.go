// Story 6.2 — gateway routing-decision integration at the handler boundary
// (6.2-INT-001..004 + meta-model + Q-G fail-open/closed). Uses a fake routing
// ClientHandle (no transport) + a fake adapter handle, so the full decision →
// dispatch → X-He-Selected-Model path is exercised without docker.
package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// fakeRoutingClient is a programmable routingclient.ClientHandle.
type fakeRoutingClient struct {
	resp   *routingv1.SelectModelResponse
	err    error
	gotReq *routingv1.SelectModelRequest
}

func (f *fakeRoutingClient) SelectModel(ctx context.Context, req *routingv1.SelectModelRequest, _ http.Header) (*routingv1.SelectModelResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func routedResp(model string, strat routingv1.Strategy, src string) *routingv1.SelectModelResponse {
	return &routingv1.SelectModelResponse{SelectedModel: model, StrategyUsed: strat, ScoreSource: src}
}

// doRoutedRequest invokes the handler with an optional X-He-Routing-Strategy
// header and the standard bearer context.
func doRoutedRequest(t *testing.T, h *handlers.ChatCompletionsHandler, body, strategyHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer he-test-key-stub")
	if strategyHeader != "" {
		req.Header.Set(routingclient.RoutingStrategyHeader, strategyHeader)
	}
	req = req.WithContext(withBearerCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func routedHandler(t *testing.T, rc routingclient.ClientHandle, reg *adapterclient.Registry) *handlers.ChatCompletionsHandler {
	t.Helper()
	opts := []handlers.ChatHandlerOption{handlers.WithRouter(routingclient.NewDecider(rc, nil))}
	if reg != nil {
		opts = append(opts, handlers.WithAdapterRegistry(reg))
	}
	return handlers.NewChatCompletionsHandler(nil, opts...)
}

// 6.2-INT-001 — non-stream: SelectModel returns selected_model; the adapter is
// resolved via the ROUTED model (NOT req.Model); X-He-Selected-Model = selected.
func TestRouting_NonStream_AdapterResolvedBySelected(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh, // registered under the SELECTED model, not the requested meta-model
	})
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	h := routedHandler(t, rc, reg)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fh.called != 1 {
		t.Fatalf("adapter handle called %d, want 1 (resolved by selected_model)", fh.called)
	}
	if fh.lastReq.GetModel() != "deepseek-v3" {
		t.Errorf("adapter req.Model = %q, want deepseek-v3 (the routed model)", fh.lastReq.GetModel())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "deepseek-v3" {
		t.Errorf("X-He-Selected-Model = %q, want deepseek-v3", got)
	}
	// routing-svc saw the meta-model as a strategy directive (requested_model="").
	if rc.gotReq.GetStrategy() != routingv1.Strategy_STRATEGY_COST || rc.gotReq.GetRequestedModel() != "" {
		t.Errorf("SelectModel req = {strategy:%v, requested:%q}, want {COST, \"\"}", rc.gotReq.GetStrategy(), rc.gotReq.GetRequestedModel())
	}
}

// 6.2-INT-004 — concrete model + no strategy → DEFAULT, selected == req.Model
// byte-for-byte (zero regression for the 6-vendor matrix).
func TestRouting_ConcreteNoStrategy_ZeroRegression(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_DEFAULT, "default")}
	h := routedHandler(t, rc, reg)

	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rc.gotReq.GetStrategy() != routingv1.Strategy_STRATEGY_DEFAULT || rc.gotReq.GetRequestedModel() != "deepseek-v3" {
		t.Errorf("SelectModel req = {strategy:%v, requested:%q}, want {DEFAULT, deepseek-v3}", rc.gotReq.GetStrategy(), rc.gotReq.GetRequestedModel())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "deepseek-v3" {
		t.Errorf("X-He-Selected-Model = %q, want deepseek-v3", got)
	}
}

// 6.2-INT-003 — mock path routes through the decision; header present & truthful.
func TestRouting_MockPath_HeaderEqualsDecision(t *testing.T) {
	// No adapter registered → mock path. Routing maps he-router-quality →
	// qwen-max (degraded). Mock echoes the selected model + sets the header.
	rc := &fakeRoutingClient{resp: routedResp("qwen-max", routingv1.Strategy_STRATEGY_QUALITY, "fallback")}
	h := routedHandler(t, rc, nil)

	rr := doRoutedRequest(t, h, `{"model":"he-router-quality","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "qwen-max" {
		t.Errorf("X-He-Selected-Model = %q, want qwen-max (mock path invariant)", got)
	}
	body := decodeBody(t, rr)
	if body["model"] != "qwen-max" {
		t.Errorf("body.model = %v, want qwen-max (routed)", body["model"])
	}
	choices := body["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != handlers.MockContent {
		t.Errorf("content = %v, want mock content", msg["content"])
	}
}

// 6.2-INT-012 / 6.2-UNIT-031 — routing-svc down + meta-model → fail-CLOSED 502.
func TestRouting_FailClosed_MetaModel502(t *testing.T) {
	rc := &fakeRoutingClient{err: connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)}
	h := routedHandler(t, rc, nil)
	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "502_upstream_unavailable" {
		t.Errorf("error.code = %v, want 502_upstream_unavailable", errObj["code"])
	}
	if rr.Header().Get("X-He-Selected-Model") != "" {
		t.Errorf("X-He-Selected-Model must be absent on the fail-closed error path")
	}
}

// 6.2-INT-012 / 6.2-UNIT-030 — routing-svc down + concrete model → fail-OPEN
// passthrough: chat keeps working, header = req.Model.
func TestRouting_FailOpen_ConcretePassthrough(t *testing.T) {
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": fh,
	})
	rc := &fakeRoutingClient{err: connect.NewError(connect.CodeUnavailable, context.DeadlineExceeded)}
	h := routedHandler(t, rc, reg)

	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open); body=%s", rr.Code, rr.Body.String())
	}
	if fh.called != 1 {
		t.Errorf("adapter not dispatched on fail-open passthrough")
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "deepseek-v3" {
		t.Errorf("X-He-Selected-Model = %q, want deepseek-v3 (passthrough)", got)
	}
}

// 6.2-INT-002 / 6.2-BLIND-FLOW-001 — stream path: decision resolved, header set
// before the first SSE chunk; mock stream emits SSE.
func TestRouting_StreamPath_HeaderBeforeChunks(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("qwen-max", routingv1.Strategy_STRATEGY_LATENCY, "fallback")}
	h := routedHandler(t, rc, nil) // no adapter → mock stream

	rr := doRoutedRequest(t, h, `{"model":"he-router-latency","messages":[{"role":"user","content":"Hi"}],"stream":true}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "qwen-max" {
		t.Errorf("X-He-Selected-Model = %q, want qwen-max (set before first chunk)", got)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rr.Body.String(), "data:") {
		t.Errorf("stream body has no SSE events: %s", rr.Body.String())
	}
}

// 6.2-UNIT-029 — routing-svc NotFound + concrete model → 400_invalid_request.
func TestRouting_ConcreteNotFound_400(t *testing.T) {
	rc := &fakeRoutingClient{err: connect.NewError(connect.CodeNotFound, context.DeadlineExceeded)}
	h := routedHandler(t, rc, nil)
	rr := doRoutedRequest(t, h, `{"model":"no-such-model","messages":[{"role":"user","content":"Hi"}]}`, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	body := decodeBody(t, rr)
	if body["error"].(map[string]any)["code"] != "400_invalid_request" {
		t.Errorf("error.code = %v, want 400_invalid_request", body["error"])
	}
}
