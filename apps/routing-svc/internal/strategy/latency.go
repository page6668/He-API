package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// Latency routes by observed p95 latency.
//
// STUB(6.1): returns the first-alphabetical-by-model-id candidate (Q-G) —
// identical to Quality/Cost by design. Story 6.2 replaces this with a
// request_logs_hourly_agg.p95_latency_ms-asc lookup.
type Latency struct{}

// compile-time cascade-lock assertion (Story 6.1 UNIT-022, BR2-1).
var _ engine.Strategy = Latency{}

// NewLatency constructs the latency strategy stub.
func NewLatency() Latency { return Latency{} }

// Select implements engine.Strategy.
func (Latency) Select(_ context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, error) {
	return firstAlphabetical(candidates)
}
