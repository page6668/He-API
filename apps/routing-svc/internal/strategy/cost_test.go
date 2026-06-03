package strategy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
)

// fakePrices is a static PriceSource for unit tests (no PG, no refresh).
type fakePrices struct{ snap *pricing.Snapshot }

func (f fakePrices) Current() *pricing.Snapshot { return f.snap }

func cands(ids ...string) []engine.ModelEntry {
	out := make([]engine.ModelEntry, len(ids))
	for i, id := range ids {
		out[i] = engine.ModelEntry{ID: id}
	}
	return out
}

func priced(m map[string]float64) fakePrices {
	return fakePrices{snap: pricing.NewSnapshot(m)}
}

// 6.2-UNIT-011 — cheapest by (input+output) sum selected.
func TestCost_CheapestSelected(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"a": 0.03, "b": 0.01, "c": 0.02}))
	got, src, err := c.SelectSourced(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "b" {
		t.Errorf("selected = %q, want b (cheapest)", got.ID)
	}
	// 6.2-UNIT-018 — score source on the cost path.
	if src != engine.ScoreSourceModelPricing {
		t.Errorf("score_source = %q, want %q", src, engine.ScoreSourceModelPricing)
	}
}

// 6.2-UNIT-012 / 6.2-UNIT-022 (tie-break) — equal price → first-alphabetical.
func TestCost_PriceTieFirstAlphabetical(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"zeta": 0.01, "alpha": 0.01, "mid": 0.01}))
	got, _, err := c.SelectSourced(context.Background(), cands("zeta", "alpha", "mid"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "alpha" {
		t.Errorf("selected = %q, want alpha (alphabetical tie-break)", got.ID)
	}
}

// 6.2-UNIT-014 / 6.2-BLIND-BOUNDARY-003 — single concrete candidate returned.
func TestCost_SingleCandidate(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"only": 9.99}))
	got, _, err := c.SelectSourced(context.Background(), cands("only"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "only" {
		t.Errorf("selected = %q, want only", got.ID)
	}
}

// 6.2-UNIT-015 — empty candidate set → ErrNoCandidates (not panic).
func TestCost_EmptyCandidates(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(nil))
	_, _, err := c.SelectSourced(context.Background(), nil, engine.SelectionHints{})
	if !errors.Is(err, engine.ErrNoCandidates) {
		t.Errorf("err = %v, want ErrNoCandidates", err)
	}
}

// 6.2-UNIT-016 / 6.2-BLIND-BOUNDARY-002 — he-router-* EXCLUDED; an
// all-he-router-* catalogue yields ErrNoCandidates; a decision NEVER returns a
// he-router-* id (BR2-5 correctness gate).
func TestCost_ExcludesHeRouterMeta(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{
		"qwen-max": 0.5, "he-router-cost": 0.0, "deepseek-v3": 0.4,
	}))
	got, _, err := c.SelectSourced(context.Background(),
		cands("he-router-cost", "qwen-max", "deepseek-v3", "he-router-quality"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "deepseek-v3" {
		t.Errorf("selected = %q, want deepseek-v3 (he-router-* must be excluded even with price 0)", got.ID)
	}

	// All-he-router-* catalogue → no concrete candidate.
	_, _, err = c.SelectSourced(context.Background(),
		cands("he-router-cost", "he-router-quality", "he-router-latency"), engine.SelectionHints{})
	if !errors.Is(err, engine.ErrNoCandidates) {
		t.Errorf("all-meta err = %v, want ErrNoCandidates", err)
	}
}

// 6.2-UNIT-017 / 6.2-BLIND-DATA-002 — candidate with NO pricing row ranks last,
// never panics, never silently dropped.
func TestCost_MissingPricingRankedLast(t *testing.T) {
	t.Parallel()
	// "b" has no pricing row → +Inf → ranked last; "a" priced wins.
	c := strategy.NewCost(priced(map[string]float64{"a": 5.0}))
	got, _, err := c.SelectSourced(context.Background(), cands("a", "b"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("selected = %q, want a (unpriced b ranked last)", got.ID)
	}

	// When ALL candidates are unpriced → all +Inf → first-alphabetical, no panic.
	got2, _, err := c.SelectSourced(context.Background(), cands("y", "x", "z"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced all-unpriced: %v", err)
	}
	if got2.ID != "x" {
		t.Errorf("all-unpriced selected = %q, want x (alphabetical)", got2.ID)
	}
}

// 6.2-BLIND-BOUNDARY-004 — a 0.0 price (free model) is a valid cheapest, NOT
// treated as "missing".
func TestCost_ZeroPriceIsCheapest(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"free": 0.0, "paid": 0.001}))
	got, _, err := c.SelectSourced(context.Background(), cands("paid", "free"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "free" {
		t.Errorf("selected = %q, want free (0.0 is a real cheapest)", got.ID)
	}
}

// nil PriceSource degrades to first-alphabetical without panic (defensive).
func TestCost_NilPriceSource(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(nil)
	got, src, err := c.SelectSourced(context.Background(), cands("beta", "alpha"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "alpha" {
		t.Errorf("selected = %q, want alpha", got.ID)
	}
	if src != engine.ScoreSourceModelPricing {
		t.Errorf("score_source = %q, want model_pricing", src)
	}
}

// Select (cascade-locked signature) returns the same selection as SelectSourced.
func TestCost_SelectMatchesSourced(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"a": 0.2, "b": 0.1}))
	got, err := c.Select(context.Background(), cands("a", "b"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.ID != "b" {
		t.Errorf("Select = %q, want b", got.ID)
	}
}
