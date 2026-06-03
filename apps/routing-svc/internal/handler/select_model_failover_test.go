// Story 6.3 AC1 — handler populates SelectModelResponse.failover_chain.
//
// Scenario trace -> docs/qa/assessments/6.3-test-design-20260603.md:
//
//	6.3-UNIT-014  select_model.go populates resp.FailoverChain from the ranked tail
//	6.3-UNIT-006  STRATEGY_DEFAULT (passthrough) -> EMPTY failover_chain (Q-D)
//	6.3-UNIT-008  no he-router-* id appears in the populated chain (BR1-3)
package handler

import (
	"testing"

	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// 6.3-UNIT-014 (P1) — a strategy-routed request returns selected_model = chain
// head + failover_chain = the ordered tail. With nil prices the cost strategy
// degrades to first-alphabetical, so {alpha,beta,gamma} → alpha + [beta,gamma].
func Test_UNIT_014_handler_populates_failover_chain(t *testing.T) {
	s := newTestServer(t, nil, "alpha", "beta", "gamma")
	resp, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_COST})
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if resp.GetSelectedModel() != "alpha" {
		t.Errorf("selected_model = %q, want alpha (chain head)", resp.GetSelectedModel())
	}
	chain := resp.GetFailoverChain()
	if len(chain) != 2 || chain[0] != "beta" || chain[1] != "gamma" {
		t.Errorf("failover_chain = %v, want [beta gamma]", chain)
	}
	// the tail never repeats the selected model (BR1-5).
	for _, m := range chain {
		if m == resp.GetSelectedModel() {
			t.Errorf("selected model %q reappears in failover_chain %v", m, chain)
		}
	}
}

// 6.3-UNIT-006 (P0) — the DEFAULT/passthrough path returns an EMPTY
// failover_chain: the user pinned a concrete model; it has no fallbacks (Q-D).
func Test_UNIT_006_handler_default_empty_failover_chain(t *testing.T) {
	s := newTestServer(t, nil, "alpha", "beta", "gamma")
	resp, err := call(s, &routingv1.SelectModelRequest{
		Strategy:       routingv1.Strategy_STRATEGY_DEFAULT,
		RequestedModel: "beta",
	})
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	if resp.GetSelectedModel() != "beta" {
		t.Errorf("selected_model = %q, want beta", resp.GetSelectedModel())
	}
	if len(resp.GetFailoverChain()) != 0 {
		t.Errorf("failover_chain = %v, want empty (Q-D pinned path)", resp.GetFailoverChain())
	}
}
