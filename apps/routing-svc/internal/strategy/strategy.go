// Package strategy holds the 4 routing-strategy implementations registered by
// routing-svc: quality, cost, latency (Story 6.1 deterministic STUBS) and
// default (passthrough). Each satisfies engine.Strategy (BR2-1).
//
// STUB NOTICE (Story 6.1, Q-G): quality / cost / latency are INTENTIONALLY
// INDISTINGUISHABLE — all three return the first-alphabetical-by-model-id
// candidate. This is deliberately "obviously a stub" so Story 6.2's QA can
// prove the real scoring replaced it (by asserting the three diverge). Story
// 6.2 replaces each with its real source (benchmark_results.quality_score /
// model_pricing / request_logs_hourly_agg.p95_latency_ms).
package strategy

import (
	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// firstAlphabetical returns the candidate with the lexicographically smallest
// model id (Q-G deterministic tie-breaker), or ErrNoCandidates when the set is
// empty. It does not mutate or require a pre-sorted input.
func firstAlphabetical(candidates []engine.ModelEntry) (engine.ModelEntry, error) {
	if len(candidates) == 0 {
		return engine.ModelEntry{}, engine.ErrNoCandidates
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.ID < best.ID {
			best = c
		}
	}
	return best, nil
}
