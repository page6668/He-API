// Engine + dispatch tests (Story 6.1 AC2 + AC1 boot validation).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-001  NewEngine rejects an empty catalogue -> construction error (BR1-2)
//	6.1-UNIT-002  NewEngine fails when a slug is registered without an impl
//	6.1-UNIT-014  STRATEGY_DEFAULT(1) -> requested_model verbatim when present
//	6.1-UNIT-015  STRATEGY_UNSPECIFIED(0) == DEFAULT; never errors on zero value (Q-D)
//	6.1-UNIT-016  enum not in registry -> ErrUnknownStrategy
//	6.1-UNIT-018  DEFAULT with requested_model not in catalogue -> ErrNoCandidates
//	6.1-UNIT-021  sentinels are exported, errors.Is-comparable (BR2-4)
//	6.1-UNIT-036  strategy_used echoes the actually-fired strategy (Q-I/Q-D)
//	6.1-BLIND-BOUNDARY-002  enum just-beyond-range (5/99) -> ErrUnknownStrategy
//	6.1-BLIND-CONCURRENCY-001  N concurrent Decide -> deterministic, no race (-race)
package engine

import (
	"context"
	"errors"
	"sync"
	"testing"

	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// testCatalogue builds a small in-memory catalogue from explicit ids.
func testCatalogue(t *testing.T, ids ...string) modelscatalogue.Catalogue {
	t.Helper()
	reg := modelscatalogue.Registry{Capabilities: map[string]modelscatalogue.Capabilities{}}
	for _, id := range ids {
		reg.Models = append(reg.Models, modelscatalogue.ModelSeed{ID: id, Vendor: "test"})
		reg.Capabilities[id] = modelscatalogue.Capabilities{Chat: true}
	}
	return modelscatalogue.NewFromRegistry(reg)
}

// pickByID is a test Strategy that returns the candidate with the configured
// id (or ErrNoCandidates) — used to assert dispatch routes to the right impl.
type pickByID struct{ id string }

func (p pickByID) Select(_ context.Context, candidates []ModelEntry, _ SelectionHints) (ModelEntry, error) {
	for _, c := range candidates {
		if c.ID == p.id {
			return c, nil
		}
	}
	return ModelEntry{}, ErrNoCandidates
}

func fullStrategies() map[routingv1.Strategy]Strategy {
	return map[routingv1.Strategy]Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: pickByID{},
		routingv1.Strategy_STRATEGY_QUALITY: pickByID{id: "a"},
		routingv1.Strategy_STRATEGY_COST:    pickByID{id: "a"},
		routingv1.Strategy_STRATEGY_LATENCY: pickByID{id: "a"},
	}
}

// 6.1-UNIT-001 (P0)
func Test_UNIT_001_NewEngine_rejects_empty_catalogue(t *testing.T) {
	_, err := NewEngine(modelscatalogue.NewFromRegistry(modelscatalogue.Registry{}), fullStrategies())
	if err == nil {
		t.Fatal("NewEngine accepted an empty catalogue; want construction error (BR1-2)")
	}
	if got := err.Error(); got != "routing-svc: models catalogue is empty" {
		t.Errorf("error = %q, want %q", got, "routing-svc: models catalogue is empty")
	}
}

// 6.1-UNIT-002 (P0)
func Test_UNIT_002_NewEngine_fails_on_slug_without_impl(t *testing.T) {
	strategies := fullStrategies()
	strategies[routingv1.Strategy_STRATEGY_QUALITY] = nil // registered, no impl
	_, err := NewEngine(testCatalogue(t, "a", "b"), strategies)
	if err == nil {
		t.Fatal("NewEngine accepted a nil-impl strategy; want construction error")
	}
	want := `routing-svc: strategy "STRATEGY_QUALITY" registered without implementation`
	if got := err.Error(); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// 6.1-UNIT-014 (P0)
func Test_UNIT_014_default_returns_requested_model_verbatim(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "alpha", "beta"), withDefault())
	sel, used, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_DEFAULT, SelectionHints{RequestedModel: "beta"})
	if err != nil {
		t.Fatalf("Decide err = %v", err)
	}
	if sel.ID != "beta" {
		t.Errorf("selected = %q, want beta", sel.ID)
	}
	if used != routingv1.Strategy_STRATEGY_DEFAULT {
		t.Errorf("strategy_used = %v, want DEFAULT", used)
	}
}

// 6.1-UNIT-015 + 6.1-UNIT-036 (P0/P1)
func Test_UNIT_015_unspecified_maps_to_default_never_errors(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "alpha", "beta"), withDefault())
	sel, used, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_UNSPECIFIED, SelectionHints{RequestedModel: "alpha"})
	if err != nil {
		t.Fatalf("zero-value strategy errored: %v (Q-D: must never error)", err)
	}
	if sel.ID != "alpha" {
		t.Errorf("selected = %q, want alpha", sel.ID)
	}
	if used != routingv1.Strategy_STRATEGY_DEFAULT {
		t.Errorf("strategy_used = %v, want DEFAULT (Q-I: echoes actually-fired)", used)
	}
}

// 6.1-UNIT-016 + 6.1-BLIND-BOUNDARY-002 (P0/P1)
func Test_UNIT_016_unknown_enum_returns_ErrUnknownStrategy(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "a"), withDefault())
	for _, slug := range []routingv1.Strategy{routingv1.Strategy(5), routingv1.Strategy(99)} {
		_, _, err := e.Decide(context.Background(), slug, SelectionHints{})
		if !errors.Is(err, ErrUnknownStrategy) {
			t.Errorf("Decide(%d) err = %v, want ErrUnknownStrategy", slug, err)
		}
	}
}

// 6.1-UNIT-018 (P0)
func Test_UNIT_018_default_miss_returns_ErrNoCandidates(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "alpha"), withDefault())
	_, _, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_DEFAULT, SelectionHints{RequestedModel: "not-in-catalogue"})
	if !errors.Is(err, ErrNoCandidates) {
		t.Errorf("err = %v, want ErrNoCandidates", err)
	}
}

// 6.1-UNIT-021 (P1)
func Test_UNIT_021_sentinels_are_comparable(t *testing.T) {
	if !errors.Is(ErrNoCandidates, ErrNoCandidates) || !errors.Is(ErrUnknownStrategy, ErrUnknownStrategy) {
		t.Fatal("sentinels not errors.Is-comparable to themselves")
	}
	if errors.Is(ErrNoCandidates, ErrUnknownStrategy) {
		t.Fatal("distinct sentinels compare equal")
	}
}

// 6.1-BLIND-CONCURRENCY-001 (P1) — run with `go test -race`.
func Test_BLIND_CONCURRENCY_001_concurrent_Decide(t *testing.T) {
	e := mustEngine(t, testCatalogue(t, "alpha", "beta", "gamma"), withDefault())
	const n = 64
	var wg sync.WaitGroup
	results := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			sel, _, err := e.Decide(context.Background(), routingv1.Strategy_STRATEGY_DEFAULT, SelectionHints{RequestedModel: "beta"})
			if err == nil {
				results[i] = sel.ID
			}
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if r != "beta" {
			t.Fatalf("result[%d] = %q, want beta (non-deterministic concurrent dispatch)", i, r)
		}
	}
}

// --- helpers ---

type engineOpt func(map[routingv1.Strategy]Strategy)

// withDefault registers a real-shaped default impl (verbatim requested_model)
// so DEFAULT-path tests exercise the verbatim/miss semantics without importing
// the strategy package (avoids an import cycle in the engine test).
func withDefault() engineOpt {
	return func(m map[routingv1.Strategy]Strategy) {
		m[routingv1.Strategy_STRATEGY_DEFAULT] = verbatimDefault{}
	}
}

type verbatimDefault struct{}

func (verbatimDefault) Select(_ context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, error) {
	for _, c := range candidates {
		if c.ID == hints.RequestedModel {
			return c, nil
		}
	}
	return ModelEntry{}, ErrNoCandidates
}

func mustEngine(t *testing.T, cat modelscatalogue.Catalogue, opts ...engineOpt) *Engine {
	t.Helper()
	strategies := map[routingv1.Strategy]Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: pickByID{},
		routingv1.Strategy_STRATEGY_QUALITY: pickByID{},
		routingv1.Strategy_STRATEGY_COST:    pickByID{},
		routingv1.Strategy_STRATEGY_LATENCY: pickByID{},
	}
	for _, o := range opts {
		o(strategies)
	}
	e, err := NewEngine(cat, strategies)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}
