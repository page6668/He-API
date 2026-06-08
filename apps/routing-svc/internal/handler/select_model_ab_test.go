// Story 6.4 AC1 — SelectModel A/B decision (ab_models -> is_ab_test +
// ab_selected_models). The single-model path is UNTOUCHED (Q-I A/B-overrides).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-007  ab_models=[a,b] concrete -> is_ab_test=true, ab_selected_models=[a,b]
//	6.4-UNIT-008  he-router-* leg          -> CodeInvalidArgument (-> gateway 400, BR1-2)
//	            unknown leg                 -> CodeInvalidArgument
//	6.4-UNIT-010  empty ab_models          -> is_ab_test=false, 6.x single-model UNCHANGED
package handler

import (
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// 6.4-UNIT-007 (P0) — ab_models populated with 2 concrete ids -> A/B decision.
func Test_AB_UNIT_007_decision(t *testing.T) {
	s := newTestServer(t, slog.Default(), "qwen-max", "deepseek-v3", "he-router-cost")
	resp, err := call(s, &routingv1.SelectModelRequest{
		AbModels: []string{"qwen-max", "deepseek-v3"},
	})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	if !resp.GetIsAbTest() {
		t.Error("is_ab_test = false, want true")
	}
	got := resp.GetAbSelectedModels()
	if len(got) != 2 || got[0] != "qwen-max" || got[1] != "deepseek-v3" {
		t.Errorf("ab_selected_models = %v, want [qwen-max deepseek-v3]", got)
	}
	// selected_model is NOT meaningful on the A/B path (the legs are the
	// selection); the gateway reads ab_selected_models, not selected_model.
}

// 6.4-UNIT-008 (P0) — a he-router-* leg is rejected as InvalidArgument (the
// gateway maps that to 400_invalid_request — BR1-2 correctness gate).
func Test_AB_UNIT_008_metaModelLeg_invalidArgument(t *testing.T) {
	s := newTestServer(t, slog.Default(), "qwen-max", "deepseek-v3", "he-router-cost")
	_, err := call(s, &routingv1.SelectModelRequest{
		AbModels: []string{"qwen-max", "he-router-cost"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// unknown leg -> InvalidArgument.
func Test_AB_unknownLeg_invalidArgument(t *testing.T) {
	s := newTestServer(t, slog.Default(), "qwen-max", "deepseek-v3")
	_, err := call(s, &routingv1.SelectModelRequest{
		AbModels: []string{"qwen-max", "no-such-model"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("err code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// 6.4-UNIT-010 (P0) — empty ab_models -> the single-model path is UNCHANGED
// (is_ab_test stays false; zero regression for 6.1/6.2/6.3).
func Test_AB_UNIT_010_emptyAbModels_singleModelUnchanged(t *testing.T) {
	s := newTestServer(t, slog.Default(), "qwen-max", "deepseek-v3")
	resp, err := call(s, &routingv1.SelectModelRequest{
		Strategy:       routingv1.Strategy_STRATEGY_DEFAULT,
		RequestedModel: "qwen-max",
	})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	if resp.GetIsAbTest() {
		t.Error("is_ab_test = true, want false (no ab_models)")
	}
	if resp.GetSelectedModel() != "qwen-max" {
		t.Errorf("selected_model = %q, want qwen-max", resp.GetSelectedModel())
	}
	if len(resp.GetAbSelectedModels()) != 0 {
		t.Errorf("ab_selected_models = %v, want empty", resp.GetAbSelectedModels())
	}
}
