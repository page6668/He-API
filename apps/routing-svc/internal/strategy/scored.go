package strategy

import (
	"context"
	"sort"

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
	ranked, source, err := rankScoredOrDegrade(ctx, candidates, scorer, prices, better)
	if err != nil {
		return engine.ModelEntry{}, source, err
	}
	return ranked[0], source, nil
}

// rankScoredOrDegrade is the Story-6.3 ranked LIFT of scoredOrDegrade (BR1-4):
// it returns the FULL concrete candidate order (rank-1 first) plus the score
// source. With Scorer data it ranks by `better` (quality desc / latency asc,
// tie → first-alphabetical), reporting score_source=clickhouse; otherwise it
// degrades DETERMINISTICALLY to the cost ordering (rankCheapest) over the SAME
// concrete set, reporting score_source=fallback (BR3-2). The he-router-* virtual
// entries are excluded from the WHOLE ranking (BR1-3). By construction ranked[0]
// == scoredOrDegrade's winner for the same input (zero regression).
func rankScoredOrDegrade(
	ctx context.Context,
	candidates []engine.ModelEntry,
	scorer scoring.Scorer,
	prices PriceSource,
	better func(a, b float64) bool,
) ([]engine.ModelEntry, string, error) {
	concrete := concreteCandidates(candidates)
	if len(concrete) == 0 {
		return nil, "", engine.ErrNoCandidates
	}

	if scorer != nil {
		if scores, hasData := scorer.Score(ctx, concrete); hasData {
			if ranked, ok := rankByScore(concrete, scores, better); ok {
				return ranked, engine.ScoreSourceClickHouse, nil
			}
			// hasData but no concrete candidate carried a score → fall through
			// to the deterministic fallback (defensive; keeps the path total).
		}
	}

	var snap *pricing.Snapshot
	if prices != nil {
		snap = prices.Current()
	}
	ranked, err := rankCheapest(concrete, snap)
	return ranked, engine.ScoreSourceFallback, err
}

// bestByScore returns the candidate with the best score per `better`, ties
// broken by first-alphabetical model id. Candidates with no score are skipped.
// found=false when no candidate carried a score. It delegates to rankByScore so
// the winner is exactly the head of the ranked order (zero regression).
func bestByScore(candidates []engine.ModelEntry, scores map[string]float64, better func(a, b float64) bool) (engine.ModelEntry, bool) {
	ranked, ok := rankByScore(candidates, scores, better)
	if !ok {
		return engine.ModelEntry{}, false
	}
	return ranked[0], true
}

// rankByScore returns the candidates ordered best-first per `better` (tie →
// first-alphabetical id); candidates carrying a score rank ahead of those that
// do not, and the unscored remainder is appended in alphabetical order so the
// chain is total + deterministic (never silently dropping a candidate — BR2-3).
// ok=false when NO candidate carried a score (caller degrades to cost ordering).
func rankByScore(candidates []engine.ModelEntry, scores map[string]float64, better func(a, b float64) bool) ([]engine.ModelEntry, bool) {
	scored := make([]engine.ModelEntry, 0, len(candidates))
	unscored := make([]engine.ModelEntry, 0, len(candidates))
	for _, c := range candidates {
		if _, ok := scores[c.ID]; ok {
			scored = append(scored, c)
		} else {
			unscored = append(unscored, c)
		}
	}
	if len(scored) == 0 {
		return nil, false
	}
	sort.SliceStable(scored, func(i, j int) bool {
		si, sj := scores[scored[i].ID], scores[scored[j].ID]
		if si != sj {
			return better(si, sj)
		}
		return scored[i].ID < scored[j].ID // deterministic tie-break
	})
	sort.SliceStable(unscored, func(i, j int) bool { return unscored[i].ID < unscored[j].ID })
	return append(scored, unscored...), true
}
