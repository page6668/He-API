package strategy

import (
	"math"
	"strings"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
)

// MetaModelPrefix marks the he-router-* virtual catalogue entries
// (he-router-cost / -quality / -latency). They are routing DIRECTIVES, not
// routable upstream targets — every scoring path MUST exclude them from
// candidacy (Q-D / BR2-5 correctness gate): a he-router-* id returned as
// selected_model would miss adapterRegistry.Resolve on the gateway hot path.
const MetaModelPrefix = "he-router-"

// PriceSource yields the current immutable pricing snapshot. *pricing.Provider
// satisfies it; the cost strategy reads it per decision so a 60s refresh
// (Q-E) is picked up without re-registering the strategy.
type PriceSource interface {
	Current() *pricing.Snapshot
}

// concreteCandidates returns the candidates with the he-router-* virtual
// entries removed (BR2-5). It never mutates the input and preserves order.
func concreteCandidates(candidates []engine.ModelEntry) []engine.ModelEntry {
	out := make([]engine.ModelEntry, 0, len(candidates))
	for _, c := range candidates {
		if strings.HasPrefix(c.ID, MetaModelPrefix) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// cheapest ranks the concrete candidates by (input+output) price ascending
// (Q-J) using snap; a candidate with no pricing row is treated as +Inf and
// ranked LAST (BR2-3, never dropped, never a panic); ties resolve by
// first-alphabetical model id (Q-J / Story-6.1 tie-break). It returns
// ErrNoCandidates when no concrete candidate exists (e.g. an all-he-router-*
// catalogue — BLIND-BOUNDARY-002). A nil snap prices every candidate as +Inf,
// so the result degrades to first-alphabetical (the cost-ordering identity used
// by the quality/latency fallback when pricing is also empty).
func cheapest(candidates []engine.ModelEntry, snap *pricing.Snapshot) (engine.ModelEntry, error) {
	concrete := concreteCandidates(candidates)
	if len(concrete) == 0 {
		return engine.ModelEntry{}, engine.ErrNoCandidates
	}
	best := concrete[0]
	bestPrice := priceOrInf(snap, best.ID)
	for _, c := range concrete[1:] {
		p := priceOrInf(snap, c.ID)
		switch {
		case p < bestPrice:
			best, bestPrice = c, p
		case p == bestPrice && c.ID < best.ID:
			best = c // deterministic tie-break: first-alphabetical
		}
	}
	return best, nil
}

// priceOrInf returns the price sum for id, or +Inf when snap is nil or has no
// row for id (so missing pricing ranks last without special-casing in cheapest).
func priceOrInf(snap *pricing.Snapshot, id string) float64 {
	if snap != nil {
		if sum, ok := snap.PriceOf(id); ok {
			return sum
		}
	}
	return math.Inf(1)
}
