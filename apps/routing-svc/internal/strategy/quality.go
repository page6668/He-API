package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
)

// Quality routes by model quality (benchmark_results.quality_score, descending).
//
// DEGRADED-UNTIL-EPIC-9 (Q-A Option A): the quality backing store is a
// ClickHouse table that does not exist until Epic 9, so the default Scorer
// (scoring.NoData) reports no data and this strategy degrades DETERMINISTICALLY
// to the cost ordering (score_source=fallback). A real Epic-9 Scorer drops in
// behind the scoring.Scorer seam with no change here (BR3-1/3-4).
type Quality struct {
	scorer scoring.Scorer
	prices PriceSource
}

// compile-time cascade-lock assertions (Story 6.1 BR2-1 + Story 6.2 score-source).
var (
	_ engine.Strategy        = Quality{}
	_ engine.SourcedStrategy = Quality{}
)

// NewQuality constructs the quality strategy. A nil scorer is treated as
// no-data (always degrade); prices backs the degraded cost ordering.
func NewQuality(scorer scoring.Scorer, prices PriceSource) Quality {
	return Quality{scorer: scorer, prices: prices}
}

// Select implements engine.Strategy (delegates to SelectSourced).
func (q Quality) Select(ctx context.Context, candidates []engine.ModelEntry, hints engine.SelectionHints) (engine.ModelEntry, error) {
	m, _, err := q.SelectSourced(ctx, candidates, hints)
	return m, err
}

// SelectSourced ranks by quality_score DESC when data is available, else
// degrades to the cost ordering. higher score wins.
func (q Quality) SelectSourced(ctx context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, string, error) {
	return scoredOrDegrade(ctx, candidates, q.scorer, q.prices, func(a, b float64) bool { return a > b })
}
