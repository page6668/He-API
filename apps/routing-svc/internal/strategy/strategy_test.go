// Strategy-stub boundary tests — the headline `单测覆盖 3 种策略边界` (Story 6.1 AC2).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-010/011/012  quality/cost/latency stub -> first-alphabetical (Q-G)
//	6.1-UNIT-013          quality == cost == latency identical result (Q-G)
//	6.1-UNIT-017          empty candidate set -> ErrNoCandidates
//	6.1-UNIT-019          single-candidate -> that entry verbatim for every strategy
//	6.1-UNIT-020          duplicate/tied candidates -> first-alphabetical deterministic
//	6.1-UNIT-022          compile-time var _ engine.Strategy for all 4 stubs (in *.go)
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

// named strategies under test (default is exercised separately — different
// semantics).
func namedStrategies() map[string]engine.Strategy {
	return map[string]engine.Strategy{
		"quality": NewQuality(),
		"cost":    NewCost(),
		"latency": NewLatency(),
	}
}

// 6.1-UNIT-010/011/012 (P0) — each named stub returns first-alphabetical.
func Test_UNIT_010_011_012_named_stubs_first_alphabetical(t *testing.T) {
	cands := entries("gpt-y", "claude-x", "ernie-z") // first-alphabetical = claude-x
	for name, s := range namedStrategies() {
		got, err := s.Select(context.Background(), cands, engine.SelectionHints{})
		if err != nil {
			t.Fatalf("%s.Select err = %v", name, err)
		}
		if got.ID != "claude-x" {
			t.Errorf("%s.Select = %q, want claude-x (first-alphabetical Q-G)", name, got.ID)
		}
	}
}

// 6.1-UNIT-013 (P0) — quality == cost == latency for the same catalogue.
func Test_UNIT_013_named_stubs_indistinguishable(t *testing.T) {
	cands := entries("gpt-y", "claude-x", "ernie-z")
	q, _ := NewQuality().Select(context.Background(), cands, engine.SelectionHints{})
	c, _ := NewCost().Select(context.Background(), cands, engine.SelectionHints{})
	l, _ := NewLatency().Select(context.Background(), cands, engine.SelectionHints{})
	if q.ID != c.ID || c.ID != l.ID {
		t.Errorf("stubs diverge: quality=%q cost=%q latency=%q (Q-G: must be identical in 6.1)", q.ID, c.ID, l.ID)
	}
}

// 6.1-UNIT-017 (P0) — empty candidate set -> ErrNoCandidates (every strategy).
func Test_UNIT_017_empty_candidates_ErrNoCandidates(t *testing.T) {
	all := namedStrategies()
	all["default"] = NewDefault()
	for name, s := range all {
		_, err := s.Select(context.Background(), nil, engine.SelectionHints{RequestedModel: "x"})
		if !errors.Is(err, engine.ErrNoCandidates) {
			t.Errorf("%s.Select(empty) err = %v, want ErrNoCandidates", name, err)
		}
	}
}

// 6.1-UNIT-019 (P1) — single-candidate -> that entry for every strategy.
func Test_UNIT_019_single_candidate(t *testing.T) {
	cands := entries("only-one")
	named := namedStrategies()
	for name, s := range named {
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

// 6.1-UNIT-020 (P1) — duplicate/tied ids resolve deterministically to the
// first-alphabetical (order-independent).
func Test_UNIT_020_duplicate_tie_breaker_deterministic(t *testing.T) {
	a := entries("m-b", "m-a", "m-c", "m-a")
	b := entries("m-c", "m-a", "m-b", "m-a")
	q := NewQuality()
	r1, _ := q.Select(context.Background(), a, engine.SelectionHints{})
	r2, _ := q.Select(context.Background(), b, engine.SelectionHints{})
	if r1.ID != "m-a" || r2.ID != "m-a" {
		t.Errorf("tie-breaker not deterministic-first-alphabetical: r1=%q r2=%q, want m-a", r1.ID, r2.ID)
	}
}
