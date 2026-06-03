// Shared strategy invariants. Story 6.2 REPLACED the Story-6.1 first-alphabetical
// stubs with real scoring, so the 6.1 "indistinguishable stub" assertions
// (6.1-UNIT-010..013) are intentionally retired (Q-G: 6.2 proves the strategies
// diverge — see cost_test.go / quality_latency_test.go). The boundary
// invariants that survive the cutover are kept here with their original 6.1
// scenario ids, updated to the Story-6.2 constructors.
//
// Scenario trace -> docs/qa/assessments/6.2-test-design-20260603.md:
//
//	6.2-UNIT-015 / 6.1-UNIT-017  empty candidate set -> ErrNoCandidates (all strategies)
//	6.2-BLIND-BOUNDARY-003       single-candidate -> that entry verbatim
//	6.1-UNIT-022                 compile-time var _ engine.Strategy / SourcedStrategy (in *.go)
package strategy

import (
	"context"
	"errors"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

func entries(ids ...string) []engine.ModelEntry {
	out := make([]engine.ModelEntry, len(ids))
	for i, id := range ids {
		out[i] = engine.ModelEntry{ID: id, Vendor: "test"}
	}
	return out
}

// namedStrategies returns the 3 real named strategies with zero deps (nil
// prices + nil scorers → fully degraded), exercising the default Epic-6 path.
func namedStrategies() map[string]engine.Strategy {
	return map[string]engine.Strategy{
		"quality": NewQuality(nil, nil),
		"cost":    NewCost(nil),
		"latency": NewLatency(nil, nil),
	}
}

// 6.2-UNIT-015 / 6.1-UNIT-017 — empty candidate set -> ErrNoCandidates.
func Test_empty_candidates_ErrNoCandidates(t *testing.T) {
	t.Parallel()
	all := namedStrategies()
	all["default"] = NewDefault()
	for name, s := range all {
		_, err := s.Select(context.Background(), nil, engine.SelectionHints{RequestedModel: "x"})
		if !errors.Is(err, engine.ErrNoCandidates) {
			t.Errorf("%s.Select(empty) err = %v, want ErrNoCandidates", name, err)
		}
	}
}

// 6.2-BLIND-BOUNDARY-003 — single concrete candidate -> that entry for every
// named strategy (degraded; no pricing/score data needed).
func Test_single_candidate(t *testing.T) {
	t.Parallel()
	cands := entries("only-one")
	for name, s := range namedStrategies() {
		got, err := s.Select(context.Background(), cands, engine.SelectionHints{})
		if err != nil || got.ID != "only-one" {
			t.Errorf("%s.Select(single) = %q, err=%v; want only-one", name, got.ID, err)
		}
	}
	// default returns it only when requested verbatim.
	got, err := NewDefault().Select(context.Background(), cands, engine.SelectionHints{RequestedModel: "only-one"})
	if err != nil || got.ID != "only-one" {
		t.Errorf("default.Select(single, requested=only-one) = %q, err=%v", got.ID, err)
	}
}

// Under the default degraded path (no scorer data, no pricing), all three named
// strategies converge to the same first-alphabetical result — but via the cost
// ordering, not the retired stub. This documents the degraded-path convergence
// without asserting the retired stub semantics.
func Test_degraded_named_strategies_converge_to_cost_ordering(t *testing.T) {
	t.Parallel()
	cands := entries("gpt-y", "claude-x", "ernie-z") // all unpriced -> +Inf -> alphabetical
	q, _ := NewQuality(nil, nil).Select(context.Background(), cands, engine.SelectionHints{})
	c, _ := NewCost(nil).Select(context.Background(), cands, engine.SelectionHints{})
	l, _ := NewLatency(nil, nil).Select(context.Background(), cands, engine.SelectionHints{})
	if q.ID != "claude-x" || c.ID != "claude-x" || l.ID != "claude-x" {
		t.Errorf("degraded convergence: quality=%q cost=%q latency=%q, want claude-x", q.ID, c.ID, l.ID)
	}
}
