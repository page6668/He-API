package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
)

// scoredOrDegrade is the shared quality/latency decision body (AC3). It
// consults the Scorer over the concrete candidate set (he-router-* excluded —
// BR2-5 applies to the degraded path too); if the Scorer has data it ranks by
// `better` (quality desc / latency asc, tie → first-alphabetical) and reports
// score_source=clickhouse; otherwise it degrades DETERMINISTICALLY to the cost
// ordering (BR3-2) over the SAME candidate set and reports score_source=fallback.
//
// strategy_used is NOT decided here — the engine echoes the requested strategy
// enum verbatim (BR3-3 truthfulness); degradation is observable ONLY via the
// score_source returned here.
func scoredOrDegrade(
	ctx context.Context,
	candidates []engine.ModelEntry,
	scorer scoring.Scorer,
	prices PriceSource,
	better func(a, b float64) bool,
) (engine.ModelEntry, string, error) {
	concrete := concreteCandidates(candidates)
	if len(concrete) == 0 {
		return engine.ModelEntry{}, "", engine.ErrNoCandidates
	}

	if scorer != nil {
		if scores, hasData := scorer.Score(ctx, concrete); hasData {
			if best, ok := bestByScore(concrete, scores, better); ok {
				return best, engine.ScoreSourceClickHouse, nil
			}
			// hasData but no concrete candidate carried a score → fall through
			// to the deterministic fallback (defensive; keeps the path total).
		}
	}

	var snap *pricing.Snapshot
	if prices != nil {
		snap = prices.Current()
	}
	m, err := cheapest(concrete, snap)
	return m, engine.ScoreSourceFallback, err
}

// bestByScore returns the candidate with the best score per `better`, ties
// broken by first-alphabetical model id. Candidates with no score are skipped.
// found=false when no candidate carried a score.
func bestByScore(candidates []engine.ModelEntry, scores map[string]float64, better func(a, b float64) bool) (engine.ModelEntry, bool) {
	var best engine.ModelEntry
	var bestScore float64
	found := false
	for _, c := range candidates {
		s, ok := scores[c.ID]
		if !ok {
			continue
		}
		switch {
		case !found, better(s, bestScore):
			best, bestScore, found = c, s, true
		case s == bestScore && c.ID < best.ID:
			best = c
		}
	}
	return best, found
}
