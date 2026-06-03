package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
)

// Cost routes by upstream price: the candidate with the lowest
// (upstream_price_per_1k_input_tokens + upstream_price_per_1k_output_tokens)
// over the boot catalogue MINUS the he-router-* virtual entries (Q-D / BR2-5),
// using the latest-effective_at pricing snapshot (Q-J / BR2-2). A candidate
// with no pricing row ranks last (BR2-3); price ties resolve first-alphabetical.
//
// Story 6.2 replaces the Story-6.1 first-alphabetical stub with this real
// model_pricing scoring. It reads a Snapshot (boot + 60s refresh, Q-E) — never
// a per-request PG query (BR2-2).
type Cost struct {
	prices PriceSource
}

// compile-time cascade-lock assertions (Story 6.1 BR2-1 + Story 6.2 score-source).
var (
	_ engine.Strategy        = Cost{}
	_ engine.SourcedStrategy = Cost{}
)

// NewCost constructs the cost strategy over a pricing source. A nil source
// prices every candidate as +Inf → the selection degrades to first-alphabetical
// (still deterministic, still he-router-*-excluded), so a mis-wired source
// never panics the hot path.
func NewCost(prices PriceSource) Cost { return Cost{prices: prices} }

// Select implements engine.Strategy (delegates to SelectSourced, dropping the
// score source — preserves the cascade-locked signature for 6.3/6.4).
func (c Cost) Select(ctx context.Context, candidates []engine.ModelEntry, hints engine.SelectionHints) (engine.ModelEntry, error) {
	m, _, err := c.SelectSourced(ctx, candidates, hints)
	return m, err
}

// SelectSourced implements engine.SourcedStrategy. The score source is always
// model_pricing — the cost strategy's backing source is he_api.model_pricing by
// definition (missing rows are handled by ranked-last, not a source change).
func (c Cost) SelectSourced(_ context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, string, error) {
	var snap *pricing.Snapshot
	if c.prices != nil {
		snap = c.prices.Current()
	}
	m, err := cheapest(candidates, snap)
	return m, engine.ScoreSourceModelPricing, err
}
