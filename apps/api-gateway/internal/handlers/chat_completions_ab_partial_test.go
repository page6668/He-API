// Story 6.4 AC3 — partial failure: A/B legs fail INDEPENDENTLY (Q-D/Q-E).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-030  leg 502 + leg 200 -> 200 merged: success + failed-leg marker
//	6.4-UNIT-032  leg 502           -> single attempt, NO failover_chain loop (Q-D)
//	6.4-UNIT-033  leg 502 + leg 200 -> bill == success leg total only (Q-F)
//	6.4-UNIT-035  both 502          -> ONE terminal envelope; zero billing
//	6.4-UNIT-036  both fail, ≥1 504 -> representative 504 (504>502)
//	6.4-UNIT-038  leg adapter unresolved -> failed-leg 502 marker, NO panic
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
)

// 6.4-UNIT-030 + 6.4-UNIT-033 (P0) — one leg 502, other 200: 200 merged with a
// success choice + a failed-leg marker; bill only the success leg; the
// X-He-AB-Models header lists ONLY the served id.
func TestAB_PartialFailure_OneLegDown(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A-answer", 7, 11) // total 18
	legB := &failingHandle{code: connect.CodeUnavailable}      // 502
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := abHandler(t, reg, dd)

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (best-effort ≥1-leg); body=%s", rr.Code, rr.Body.String())
	}
	if legB.called != 1 {
		t.Errorf("leg B called %d, want 1 (single attempt, NO per-leg failover — Q-D)", legB.called)
	}
	var b abBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if len(b.Choices) != 2 {
		t.Fatalf("len(choices) = %d, want 2 (success + marker)", len(b.Choices))
	}
	// choices[0] = the success leg.
	if b.Choices[0].XHeModel != "qwen-max" || b.Choices[0].Message.Content != "A-answer" {
		t.Errorf("choices[0] = %+v, want success leg qwen-max", b.Choices[0])
	}
	// choices[1] = the failed-leg marker.
	mk := b.Choices[1]
	if mk.XHeModel != "deepseek-v3" || mk.FinishReason != "he_upstream_error" || mk.XHeError == nil {
		t.Fatalf("choices[1] marker = %+v, want {deepseek-v3, he_upstream_error, x_he_error!=nil}", mk)
	}
	if mk.XHeError.Code != "502_upstream_unavailable" {
		t.Errorf("marker x_he_error.code = %q, want 502_upstream_unavailable", mk.XHeError.Code)
	}
	// usage = success leg only; X-He-AB-Models = served id only; bill = 18.
	if b.Usage.TotalTokens != 18 {
		t.Errorf("usage.total = %d, want 18 (success leg only)", b.Usage.TotalTokens)
	}
	if got := rr.Header().Get("X-He-AB-Models"); got != "qwen-max" {
		t.Errorf("X-He-AB-Models = %q, want qwen-max (served only)", got)
	}
	if dd.calls != 1 || dd.lastTokens != 18 {
		t.Errorf("TPMDeduct = (%d calls, %d tokens), want (1, 18) — bill success leg only (Q-F)", dd.calls, dd.lastTokens)
	}
}

// 6.4-UNIT-035 (P0) — both legs 502: ONE terminal §5.1.2 envelope; zero billing.
func TestAB_BothFail_Terminal502(t *testing.T) {
	legA := &failingHandle{code: connect.CodeUnavailable}
	legB := &failingHandle{code: connect.CodeUnavailable}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := abHandler(t, reg, dd)

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (both-fail terminal)", rr.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Error.Code != "502_upstream_unavailable" {
		t.Errorf("error.code = %q, want 502_upstream_unavailable", env.Error.Code)
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0 (both failed — zero billing)", dd.calls)
	}
}

// 6.4-UNIT-036 (P0) — both fail, one timed out: representative code 504 (504>502).
func TestAB_BothFail_RepresentativeCode504(t *testing.T) {
	legA := &failingHandle{code: connect.CodeUnavailable}      // 502
	legB := &failingHandle{code: connect.CodeDeadlineExceeded} // 504
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	h := abHandler(t, reg, &countingDeducter{})

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (representative 504>502)", rr.Code)
	}
}

// 6.4-UNIT-038 (P0) — a leg whose adapter does not resolve is a FAILED leg with
// a 502 marker (NOT a panic); the resolvable leg still serves.
func TestAB_UnresolvedLeg_MarkerNoPanic(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A", 3, 4) // total 7
	// only qwen-max registered; deepseek-v3 is unresolved.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA,
	})
	dd := &countingDeducter{}
	h := abHandler(t, reg, dd)

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (success leg + unresolved marker); body=%s", rr.Code, rr.Body.String())
	}
	var b abBody
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(b.Choices) != 2 {
		t.Fatalf("len(choices) = %d, want 2", len(b.Choices))
	}
	mk := b.Choices[1]
	if mk.XHeModel != "deepseek-v3" || mk.FinishReason != "he_upstream_error" || mk.XHeError == nil {
		t.Fatalf("unresolved-leg marker = %+v, want he_upstream_error + x_he_error", mk)
	}
	if mk.XHeError.Code != "502_upstream_unavailable" {
		t.Errorf("unresolved marker code = %q, want 502_upstream_unavailable", mk.XHeError.Code)
	}
	if dd.calls != 1 || dd.lastTokens != 7 {
		t.Errorf("TPMDeduct = (%d, %d), want (1, 7) — only the resolvable leg billed", dd.calls, dd.lastTokens)
	}
}

// 6.4-UNIT-038b (BR3-4) — when the adapter registry is UNSET (nil), resolveLeg
// treats BOTH legs as unresolved → both-fail terminal 502, zero billing, NO
// panic. Guards the `h.adapterRegistry == nil` defensive branch (distinct from
// the model-unregistered branch covered by TestAB_UnresolvedLeg_MarkerNoPanic).
func TestAB_NilRegistry_BothUnresolvedTerminal(t *testing.T) {
	dd := &countingDeducter{}
	h := abHandler(t, nil, dd) // WithAdapterRegistry(nil) → registry unset

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (both legs unresolved, registry unset); body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Error.Code != "502_upstream_unavailable" {
		t.Errorf("error.code = %q, want 502_upstream_unavailable", env.Error.Code)
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0 (nothing served — zero billing)", dd.calls)
	}
}

// 6.4-BLIND — a non-retriable leg error (e.g. 400_content_filter / InvalidArgument)
// is surfaced in that leg's x_he_error marker; the other leg is still served
// (best-effort, BR3-5). The legs do NOT failover.
func TestAB_NonRetriableLeg_SurfacedInMarker(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A", 2, 2)
	legB := &failingHandle{code: connect.CodeInvalidArgument} // non-retriable -> 400
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	h := abHandler(t, reg, &countingDeducter{})

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (other leg served); body=%s", rr.Code, rr.Body.String())
	}
	if legB.called != 1 {
		t.Errorf("leg B called %d, want 1 (single attempt, no failover)", legB.called)
	}
	var b abBody
	_ = json.Unmarshal(rr.Body.Bytes(), &b)
	if len(b.Choices) != 2 || b.Choices[1].XHeError == nil {
		t.Fatalf("want success + failed-leg marker; got %+v", b.Choices)
	}
	if b.Choices[1].XHeError.Code != "400_invalid_request" {
		t.Errorf("marker code = %q, want 400_invalid_request (surfaced unchanged)", b.Choices[1].XHeError.Code)
	}
}
