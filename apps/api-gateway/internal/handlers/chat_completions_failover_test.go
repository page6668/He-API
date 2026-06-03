// Story 6.3 AC2 — gateway sequential failover on the NON-STREAMING path.
//
// Scenario trace -> docs/qa/assessments/6.3-test-design-20260603.md:
//
//	6.3-UNIT-015  502 on rank-1 -> advance -> success on rank-2; header=rank-2
//	6.3-UNIT-016  504 on rank-1 -> advance to rank-2
//	6.3-UNIT-017  primary + 2 hops all 502 -> 3-attempt cap -> terminal last error
//	6.3-UNIT-018  30s budget exhausted mid-chain (small budget + blocking hop) -> terminal
//	6.3-UNIT-019  non-retriable 400_invalid_request -> TERMINAL, no failover
//	6.3-UNIT-023  concrete-default (empty chain) 502 -> TERMINAL, no failover (Q-D)
//	6.3-UNIT-024  body `model` echo == FINAL served model (Q-F)
//	6.3-UNIT-025  exactly-ONE TPMDeduct across a 2-fail-then-succeed request (Q-H)
//	6.3-UNIT-026  ZERO TPMDeduct on full exhaustion
//	6.3-UNIT-027  unresolved chain entry SKIPPED, not counted against the cap (m-1/BR2-3)
//	6.3-UNIT-032  MaxFailoverAttempts=3 / FailoverBudget=30s constants
//	6.3-UNIT-046  happy path (first-attempt success) — zero failover, one attempt
//	6.3-BLIND-BOUNDARY-003/004 chain-length boundaries
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// --- failover test fixtures -------------------------------------------------

// countingDeducter records every TPMDeduct call (Q-H billing-invariant probe).
type countingDeducter struct {
	calls      int
	lastTokens int
	lastKey    string
}

func (d *countingDeducter) TPMDeduct(_ context.Context, apiKeyID string, tokens int) {
	d.calls++
	d.lastTokens = tokens
	d.lastKey = apiKeyID
}

// failingHandle returns a fixed connect error on every Chat call (an upstream
// fault). It counts invocations so the loop's attempt accounting is observable.
type failingHandle struct {
	code   connect.Code
	called int
}

func (h *failingHandle) Chat(_ context.Context, _ *adapterv1.ChatRequest, _ http.Header) (adapterclient.Stream, error) {
	h.called++
	return nil, connect.NewError(h.code, errFakeUpstream)
}

// blockingHandle blocks until the (budgeted) ctx is done, then returns its error
// — models a slow upstream that the 30s wall-clock budget cancels (6.3-UNIT-018).
type blockingHandle struct{ called int }

func (h *blockingHandle) Chat(ctx context.Context, _ *adapterv1.ChatRequest, _ http.Header) (adapterclient.Stream, error) {
	h.called++
	<-ctx.Done()
	return nil, ctx.Err()
}

var errFakeUpstream = errors.New("upstream down")

// routedRespChain builds a SelectModelResponse with selected_model + the ordered
// failover tail (Story 6.3).
func routedRespChain(selected string, chain ...string) *routingv1.SelectModelResponse {
	return &routingv1.SelectModelResponse{
		SelectedModel: selected,
		StrategyUsed:  routingv1.Strategy_STRATEGY_COST,
		ScoreSource:   "model_pricing",
		FailoverChain: chain,
	}
}

// failoverHandler wires a routing client + adapter registry + counting deducter.
func failoverHandler(t *testing.T, rc routingclient.ClientHandle, reg *adapterclient.Registry, dd handlers.TokenDeducter, opts ...handlers.ChatHandlerOption) *handlers.ChatCompletionsHandler {
	t.Helper()
	base := []handlers.ChatHandlerOption{
		handlers.WithRouter(routingclient.NewDecider(rc, nil)),
		handlers.WithAdapterRegistry(reg),
	}
	if dd != nil {
		base = append(base, handlers.WithTokenDeducter(dd))
	}
	base = append(base, opts...)
	return handlers.NewChatCompletionsHandler(nil, base...)
}

func okHandle() *fakeHandle { return newSingleChunkHandle(canonicalAdapterChunk()) }

// 6.3-UNIT-015 (P0) — 502 on rank-1 → advance → success on rank-2; header=rank-2.
func TestFailover_NonStream_502_AdvancesToRank2(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	rc := &fakeRoutingClient{resp: routedRespChain("m1", "m2")}
	dd := &countingDeducter{}
	h := failoverHandler(t, rc, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m2" {
		t.Errorf("X-He-Selected-Model = %q, want m2 (final served, Q-F)", got)
	}
	if m1.called != 1 || m2.called != 1 {
		t.Errorf("calls m1=%d m2=%d, want 1/1", m1.called, m2.called)
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want 1 (BR2-4)", dd.calls)
	}
	// 6.3-UNIT-024 — body model echo = final served model.
	var resp struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if resp.Model != "m2" {
		t.Errorf("body model = %q, want m2 (Q-F)", resp.Model)
	}
}

// 6.3-UNIT-016 (P0) — 504 on rank-1 → advance to rank-2.
func TestFailover_NonStream_504_AdvancesToRank2(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeDeadlineExceeded}
	m2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, `{"model":"he-router-latency","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m2" {
		t.Errorf("header = %q, want m2", got)
	}
}

// 6.3-UNIT-017 + BLIND-BOUNDARY-004 (P0/P2) — primary + 2 hops all 502 → cap at
// 3 attempts → terminal; the 4th chain entry is never tried.
func TestFailover_NonStream_AttemptCapAtThree(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := &failingHandle{code: connect.CodeUnavailable}
	m3 := &failingHandle{code: connect.CodeUnavailable}
	m4 := &failingHandle{code: connect.CodeUnavailable}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2, "m3": m3, "m4": m4})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2", "m3", "m4")}, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if m1.called != 1 || m2.called != 1 || m3.called != 1 {
		t.Errorf("calls m1=%d m2=%d m3=%d, want 1/1/1", m1.called, m2.called, m3.called)
	}
	if m4.called != 0 {
		t.Errorf("m4 called = %d, want 0 (3-attempt cap, BR2-2)", m4.called)
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0 (no success — 6.3-UNIT-026)", dd.calls)
	}
}

// 6.3-UNIT-018 (P0) — the 30s wall-clock budget exhausted mid-chain stops the
// chain (small budget + a hop that blocks until the budget cancels it).
func TestFailover_NonStream_BudgetExhaustion(t *testing.T) {
	m1 := &blockingHandle{}
	m2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{},
		handlers.WithFailoverBudget(40*time.Millisecond))

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusGatewayTimeout && rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 504/502 (budget exhausted)", rr.Code)
	}
	if m1.called != 1 {
		t.Errorf("m1 called = %d, want 1", m1.called)
	}
	if m2.called != 0 {
		t.Errorf("m2 called = %d, want 0 (budget stopped the chain, Q-G)", m2.called)
	}
}

// 6.3-UNIT-019 (P0) — a non-retriable error (CodeInvalidArgument → 400) is
// TERMINAL immediately; no failover (Q-C).
func TestFailover_NonStream_NonRetriable_NoFailover(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeInvalidArgument}
	m2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (non-retriable terminal)", rr.Code)
	}
	if m2.called != 0 {
		t.Errorf("m2 called = %d, want 0 (NO failover on non-retriable, Q-C)", m2.called)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "400_invalid_request" {
		t.Errorf("error.code = %q, want 400_invalid_request", env.Error.Code)
	}
}

// 6.3-UNIT-020/021/022 (P0) — every non-retriable upstream code is TERMINAL
// immediately with its own §5.1.2 envelope; the next chain model is never tried
// (Q-C). Exercises the classifyFailoverError non-retriable branches.
func TestFailover_NonStream_NonRetriableCodes_Terminal(t *testing.T) {
	cases := []struct {
		name     string
		code     connect.Code
		wantHTTP int
		wantCode string
	}{
		{"permission_denied_403", connect.CodePermissionDenied, http.StatusForbidden, "403_model_not_in_scope"},
		{"resource_exhausted_429", connect.CodeResourceExhausted, http.StatusTooManyRequests, "429_rate_limit_qps"},
		{"failed_precondition_content_filter", connect.CodeFailedPrecondition, http.StatusBadRequest, "400_content_filter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m1 := &failingHandle{code: tc.code}
			m2 := okHandle()
			reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
			h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

			rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
			if rr.Code != tc.wantHTTP {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantHTTP)
			}
			if m2.called != 0 {
				t.Errorf("m2 called = %d, want 0 (non-retriable → no failover, Q-C)", m2.called)
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(rr.Body.Bytes(), &env)
			if env.Error.Code != tc.wantCode {
				t.Errorf("error.code = %q, want %q", env.Error.Code, tc.wantCode)
			}
		})
	}
}

// 6.3-UNIT-023 (P0) — a concrete-default request (empty chain — Q-D) returns
// 502/504 → TERMINAL, no failover (byte-for-byte today's behaviour).
func TestFailover_NonStream_ConcreteDefault_EmptyChain_NoFailover(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1})
	// passthrough decider (nil client) → empty failover chain; selected == model.
	h := handlers.NewChatCompletionsHandler(nil,
		handlers.WithAdapterRegistry(reg))

	rr := doRoutedRequest(t, h, `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (concrete-default terminal)", rr.Code)
	}
	if m1.called != 1 {
		t.Errorf("m1 called = %d, want 1 (single attempt, no failover)", m1.called)
	}
}

// 6.3-UNIT-025 (P0) — exactly-ONE TPMDeduct across a 2-fail-then-succeed request
// (BR2-4 / Q-H billing invariant).
func TestFailover_NonStream_ExactlyOneDeduct_TwoFailThenSucceed(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := &failingHandle{code: connect.CodeUnavailable}
	m3 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2, "m3": m3})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2", "m3")}, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want exactly 1 (BR2-4)", dd.calls)
	}
	if dd.lastTokens != 13 {
		t.Errorf("deducted tokens = %d, want 13 (the served response usage)", dd.lastTokens)
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m3" {
		t.Errorf("header = %q, want m3", got)
	}
}

// 6.3-UNIT-027 (P1) + m-1/BR2-3 — an unresolved chain entry is SKIPPED (not an
// upstream attempt) and the next resolvable model is tried.
func TestFailover_NonStream_UnresolvedEntrySkipped(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m3 := okHandle()
	// "m2" is in the chain but NOT registered → must be skipped, not counted.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m3": m3})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2", "m3")}, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m3" {
		t.Errorf("header = %q, want m3 (skipped unresolved m2)", got)
	}
	if m1.called != 1 || m3.called != 1 {
		t.Errorf("calls m1=%d m3=%d, want 1/1", m1.called, m3.called)
	}
}

// 6.3-UNIT-046 (P0) — happy path: first attempt succeeds → one upstream call,
// one deduction, header = the selected model, no failover.
func TestFailover_NonStream_HappyPath_ZeroFailover(t *testing.T) {
	m1 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1")}, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if m1.called != 1 {
		t.Errorf("m1 called = %d, want exactly 1 (happy path)", m1.called)
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want 1", dd.calls)
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m1" {
		t.Errorf("header = %q, want m1", got)
	}
}

// 6.3-UNIT-045 (P1) / BR4-1 — the per-hop failover slog line carries
// {from_model,to_model,reason,attempt,strategy,he_request_id} and NEVER
// user_id / message content (non-PII discipline).
func TestFailover_NonStream_FailoverSlog_NonPII(t *testing.T) {
	buf := &bytes.Buffer{}
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := handlers.NewChatCompletionsHandler(bufLogger(buf),
		handlers.WithRouter(routingclient.NewDecider(&fakeRoutingClient{resp: routedRespChain("m1", "m2")}, nil)),
		handlers.WithAdapterRegistry(reg),
		handlers.WithTokenDeducter(&countingDeducter{}),
	)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"SECRET-CONTENT"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"event":"routing_failover"`) {
		t.Fatalf("no routing_failover slog line: %s", logs)
	}
	for _, field := range []string{`"routing_action":"failover"`, `"from_model":"m1"`, `"to_model":"m2"`, `"reason":"upstream_unavailable"`, `"strategy":"STRATEGY_COST"`, `"attempt":2`} {
		if !strings.Contains(logs, field) {
			t.Errorf("routing_failover line missing %s; logs=%s", field, logs)
		}
	}
	// Non-PII: never the message content, never a user_id field.
	if strings.Contains(logs, "SECRET-CONTENT") {
		t.Error("PII LEAK — message content appeared in logs (BR4-1)")
	}
	if strings.Contains(logs, `"user_id"`) {
		t.Error("PII LEAK — user_id appeared in failover logs (BR4-1)")
	}
}

// 6.3-UNIT-032 (P0) — the loop bounds are named, tunable constants.
func TestFailover_Constants(t *testing.T) {
	if handlers.MaxFailoverAttempts != 3 {
		t.Errorf("MaxFailoverAttempts = %d, want 3", handlers.MaxFailoverAttempts)
	}
	if handlers.FailoverBudget != 30*time.Second {
		t.Errorf("FailoverBudget = %v, want 30s", handlers.FailoverBudget)
	}
}
