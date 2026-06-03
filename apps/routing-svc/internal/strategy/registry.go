package strategy

import (
	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// Deps are the runtime dependencies the real Story-6.2 strategies need:
//   - Prices       — the pricing snapshot source (cost scoring + quality/latency
//     degraded fallback). Nil → cost degrades to first-alphabetical.
//   - QualityScorer / LatencyScorer — the AC3 Scorer seam. Nil → scoring.NoData
//     (always degrade), which is the default Epic-6 behaviour until Epic 9.
type Deps struct {
	Prices        PriceSource
	QualityScorer scoring.Scorer
	LatencyScorer scoring.Scorer
}

// DefaultStrategies is the canonical strategy-slug → implementation map that
// routing-svc registers at boot. It is the single registration point consumed
// by cmd/server/main.go and the engine/integration tests, so the set of
// supported slugs never drifts between production wiring and tests.
//
// STRATEGY_UNSPECIFIED is intentionally absent: the engine maps it to
// STRATEGY_DEFAULT before dispatch (Q-D), so it needs no own entry.
//
// Story 6.2 threads real dependencies in via Deps; nil Scorers default to
// scoring.NoData so a zero-value Deps still yields a valid (fully degraded)
// engine — the cost strategy alone is data-backed by Prices.
func DefaultStrategies(deps Deps) map[routingv1.Strategy]engine.Strategy {
	qualityScorer := deps.QualityScorer
	if qualityScorer == nil {
		qualityScorer = scoring.NoData{}
	}
	latencyScorer := deps.LatencyScorer
	if latencyScorer == nil {
		latencyScorer = scoring.NoData{}
	}
	return map[routingv1.Strategy]engine.Strategy{
		routingv1.Strategy_STRATEGY_DEFAULT: NewDefault(),
		routingv1.Strategy_STRATEGY_QUALITY: NewQuality(qualityScorer, deps.Prices),
		routingv1.Strategy_STRATEGY_COST:    NewCost(deps.Prices),
		routingv1.Strategy_STRATEGY_LATENCY: NewLatency(deps.LatencyScorer, deps.Prices),
	}
}
