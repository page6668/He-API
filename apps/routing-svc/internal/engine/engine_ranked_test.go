// Story 6.3 AC1 — Engine.Decide ranked dispatch (failover tail derivation).
//
// Scenario trace -> docs/qa/assessments/6.3-test-design-20260603.md:
//
//	6.3-UNIT-006  STRATEGY_DEFAULT (plain Strategy) -> EMPTY failover tail (Q-D)
//	6.3-UNIT-007  single-candidate ranked chain -> selected + EMPTY tail
//	6.3-UNIT-010  tail = chain[1:], excludes selected, de-duplicated (BR1-5)
//	6.3-UNIT-011  strategy_used + score_source unchanged by failover depth (Q-J)
//	6.3-UNIT-012  Decide PREFERS RankedStrategy; plain Strategy -> empty tail (BR1-2)
package engine

import (
	"context"
	"testing"

	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// rankedPick is a test RankedStrategy returning a fixed ordered chain + source.
// It also satisfies Strategy/SourcedStrategy so the engine's preference order
// (Ranked > Sourced > plain) is observable.
type rankedPick struct {
	chain  []string
	source string
}

func (p rankedPick) Select(ctx context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, error) {
	m, _, err := p.SelectSourced(ctx, candidates, hints)
	return m, err
}

func (p rankedPick) SelectSourced(ctx context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, string, error) {
	chain, src, err := p.SelectRanked(ctx, candidates, hints)
	if err != nil {
		return ModelEntry{}, "", err
	}
	return chain[0], src, nil
}

func (p rankedPick) SelectRanked(_ context.Context, _ []ModelEntry, _ SelectionHints) ([]ModelEntry, string, error) {
	if len(p.chain) == 0 {
		return nil, "", ErrNoCandidates
	}
	out := make([]ModelEntry, len(p.chain))
	for i, id := range p.chain {
		out[i] = ModelEntry{ID: id}
	}
	return out, p.source, nil
}

func tailIDs(tail []ModelEntry) []string {
	out := make([]string, len(tail))
	for i, m := range tail {
		out[i] = m.ID
	}
	return out
}

// 6.3-UNIT-012 (P1) — Decide PREFERS the ranked capability; chain[0]=selected,
// chain[1:]=tail.
func Test_UNIT_012_Decide_prefers_ranked(t *testing.T) {
	strategies := map[routingv1.Strategy]Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: verbatimDefault{},
		routingv1.Strategy_STRATEGY_COST:    rankedPick{chain: []string{"b", "c", "a"}, source: ScoreSourceModelPricing},
	}
	e, err := NewEngine(testCatalogue(t, "a", "b", "c"), strategies)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sel, tail, used, src, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_COST, SelectionHints{})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if sel.ID != "b" {
		t.Errorf("selected = %q, want b (chain head)", sel.ID)
	}
	if got := tailIDs(tail); len(got) != 2 || got[0] != "c" || got[1] != "a" {
		t.Errorf("failover tail = %v, want [c a]", got)
	}
	if used != routingv1.Strategy_STRATEGY_COST {
		t.Errorf("strategy_used = %v, want COST", used)
	}
	if src != ScoreSourceModelPricing {
		t.Errorf("score_source = %q, want model_pricing", src)
	}
}

// 6.3-UNIT-006 + 6.3-UNIT-012 (P0) — a plain Strategy (DEFAULT passthrough)
// yields an EMPTY failover tail (Q-D — the pinned model has no fallbacks).
func Test_UNIT_006_default_empty_failover_tail(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "alpha", "beta"), withDefault())
	sel, tail, used, src, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_DEFAULT, SelectionHints{RequestedModel: "beta"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if sel.ID != "beta" {
		t.Errorf("selected = %q, want beta", sel.ID)
	}
	if len(tail) != 0 {
		t.Errorf("failover tail = %v, want empty (Q-D pinned path)", tailIDs(tail))
	}
	if used != routingv1.Strategy_STRATEGY_DEFAULT {
		t.Errorf("strategy_used = %v, want DEFAULT", used)
	}
	if src != ScoreSourceDefault {
		t.Errorf("score_source = %q, want default", src)
	}
}

// 6.3-UNIT-007 (P0) — a single-element ranked chain yields the winner + an
// EMPTY tail (nothing to fall back to).
func Test_UNIT_007_single_candidate_empty_tail(t *testing.T) {
	strategies := map[routingv1.Strategy]Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: verbatimDefault{},
		routingv1.Strategy_STRATEGY_COST:    rankedPick{chain: []string{"only"}, source: ScoreSourceModelPricing},
	}
	e, err := NewEngine(testCatalogue(t, "only"), strategies)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sel, tail, _, _, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_COST, SelectionHints{})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if sel.ID != "only" || len(tail) != 0 {
		t.Errorf("selected=%q tail=%v, want only + empty tail", sel.ID, tailIDs(tail))
	}
}

// 6.3-UNIT-010 (P0) — the failover tail excludes the selected model and holds
// no duplicate of it (BR1-5).
func Test_UNIT_010_tail_excludes_selected(t *testing.T) {
	strategies := map[routingv1.Strategy]Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: verbatimDefault{},
		routingv1.Strategy_STRATEGY_COST:    rankedPick{chain: []string{"b", "c", "a"}, source: ScoreSourceModelPricing},
	}
	e, err := NewEngine(testCatalogue(t, "a", "b", "c"), strategies)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	sel, tail, _, _, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_COST, SelectionHints{})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	for _, m := range tail {
		if m.ID == sel.ID {
			t.Errorf("selected %q reappears in failover tail %v (BR1-5)", sel.ID, tailIDs(tail))
		}
	}
}
