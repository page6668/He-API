// Story 6.4 AC1 — Engine.ResolveABModels concrete-catalogue gate (BR1-2).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-007  [a,b] concrete         -> returns [a,b] (no error)
//	6.4-UNIT-008  he-router-* leg        -> ErrABModelNotConcrete (correctness gate)
//	            unknown catalogue id     -> ErrABModelNotConcrete
package engine

import (
	"errors"
	"testing"
)

func abEngine(t *testing.T) *Engine {
	t.Helper()
	// Catalogue holds two concrete ids + a he-router-* virtual entry (the
	// concrete-gate MUST reject the virtual one as an A/B leg).
	cat := testCatalogue(t, "qwen-max", "deepseek-v3", "he-router-cost")
	e, err := NewEngine(cat, fullStrategies())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

// 6.4-UNIT-007 (P0) — two concrete catalogue ids resolve in input order.
func Test_AB_ResolveConcrete(t *testing.T) {
	got, err := abEngine(t).ResolveABModels([]string{"qwen-max", "deepseek-v3"})
	if err != nil {
		t.Fatalf("ResolveABModels err = %v, want nil", err)
	}
	if len(got) != 2 || got[0] != "qwen-max" || got[1] != "deepseek-v3" {
		t.Fatalf("ResolveABModels = %v, want [qwen-max deepseek-v3]", got)
	}
}

// 6.4-UNIT-008 (P0) — a he-router-* leg is NOT a routable concrete model
// (BR1-2 correctness gate) -> ErrABModelNotConcrete.
func Test_AB_RejectsMetaModelLeg(t *testing.T) {
	_, err := abEngine(t).ResolveABModels([]string{"qwen-max", "he-router-cost"})
	if !errors.Is(err, ErrABModelNotConcrete) {
		t.Fatalf("ResolveABModels(he-router-* leg) err = %v, want ErrABModelNotConcrete", err)
	}
}

// unknown id -> ErrABModelNotConcrete (each leg must be in the catalogue).
func Test_AB_RejectsUnknownLeg(t *testing.T) {
	_, err := abEngine(t).ResolveABModels([]string{"qwen-max", "no-such-model"})
	if !errors.Is(err, ErrABModelNotConcrete) {
		t.Fatalf("ResolveABModels(unknown leg) err = %v, want ErrABModelNotConcrete", err)
	}
}
