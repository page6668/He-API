package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
)

// Latency routes by observed p95 latency (request_logs_hourly_agg.p95_latency_ms,
// ascending — lower is better).
//
// DEGRADED-UNTIL-EPIC-9 (Q-A Option A): the latency backing store is a
// ClickHouse materialized view fed by request_logs, which has no writer until
// Epic 9, so the default Scorer (scoring.NoData) reports no data and this
// strategy degrades DETERMINISTICALLY to the cost ordering (score_source=
// fallback). A real Epic-9 Scorer drops in behind the scoring.Scorer seam with
// no change here (BR3-1/3-4).
type Latency struct {
	scorer scoring.Scorer
	prices PriceSource
}

// compile-time cascade-lock assertions (Story 6.1 BR2-1 + Story 6.2 score-source).
var (
	_ engine.Strategy        = Latency{}
	_ engine.SourcedStrategy = Latency{}
)

// NewLatency constructs the latency strategy. A nil scorer is treated as
// no-data (always degrade); prices backs the degraded cost ordering.
func NewLatency(scorer scoring.Scorer, prices PriceSource) Latency {
	return Latency{scorer: scorer, prices: prices}
}

// Select implements engine.Strategy (delegates to SelectSourced).
func (l Latency) Select(ctx context.Context, candidates []engine.ModelEntry, hints engine.SelectionHints) (engine.ModelEntry, error) {
	m, _, err := l.SelectSourced(ctx, candidates, hints)
	return m, err
}

// SelectSourced ranks by p95_latency_ms ASC when data is available, else
// degrades to the cost ordering. lower score wins.
func (l Latency) SelectSourced(ctx context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, string, error) {
	return scoredOrDegrade(ctx, candidates, l.scorer, l.prices, func(a, b float64) bool { return a < b })
}
