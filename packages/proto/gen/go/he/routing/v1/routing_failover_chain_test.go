package routingv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// 6.3-INT-001 — additive `failover_chain = 7` wire round-trip. Proves the
// hand-edited rawDesc (the local env cannot run buf — see Story 6.3 T0.1 env
// note) encodes/decodes field 7 identically, AND that fields 1-6 are untouched
// (backward-compatible additive change; same pattern as score_source=6).
func TestSelectModelResponse_FailoverChain_RoundTrip(t *testing.T) {
	t.Parallel()

	orig := &SelectModelResponse{
		SelectedModel:    "qwen-max",
		AdapterEndpoint:  "",
		IsAbTest:         false,
		AbSelectedModels: []string{"a", "b"},
		StrategyUsed:     Strategy_STRATEGY_COST,
		ScoreSource:      "model_pricing",
		FailoverChain:    []string{"deepseek-v3", "glm-4", "kimi-k2"},
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got SelectModelResponse
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !proto.Equal(orig, &got) {
		t.Fatalf("round-trip mismatch:\n orig = %+v\n  got = %+v", orig, &got)
	}

	// Explicit field-7 readback via the generated getter.
	gotChain := got.GetFailoverChain()
	if len(gotChain) != 3 || gotChain[0] != "deepseek-v3" || gotChain[1] != "glm-4" || gotChain[2] != "kimi-k2" {
		t.Errorf("GetFailoverChain() = %v, want [deepseek-v3 glm-4 kimi-k2]", gotChain)
	}

	// Fields 1-6 unchanged by the additive edit (backward-compat guard).
	if got.GetSelectedModel() != "qwen-max" {
		t.Errorf("selected_model = %q, want qwen-max", got.GetSelectedModel())
	}
	if got.GetStrategyUsed() != Strategy_STRATEGY_COST {
		t.Errorf("strategy_used = %v, want COST", got.GetStrategyUsed())
	}
	if got.GetScoreSource() != "model_pricing" {
		t.Errorf("score_source = %q, want model_pricing", got.GetScoreSource())
	}
	if len(got.GetAbSelectedModels()) != 2 {
		t.Errorf("ab_selected_models = %v, want len 2", got.GetAbSelectedModels())
	}
}

// An empty failover_chain (the common DEFAULT/single-candidate case — BR1-5)
// round-trips as nil/empty without emitting a spurious wire field.
func TestSelectModelResponse_FailoverChain_EmptyRoundTrip(t *testing.T) {
	t.Parallel()

	orig := &SelectModelResponse{SelectedModel: "qwen-max", StrategyUsed: Strategy_STRATEGY_DEFAULT}
	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SelectModelResponse
	if err := proto.Unmarshal(wire, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.GetFailoverChain()) != 0 {
		t.Errorf("empty chain round-trip = %v, want empty", got.GetFailoverChain())
	}
}
