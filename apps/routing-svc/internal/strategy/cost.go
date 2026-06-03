package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// Cost routes by upstream price.
//
// STUB(6.1): returns the first-alphabetical-by-model-id candidate (Q-G) —
// identical to Quality/Latency by design. Story 6.2 replaces this with a
// model_pricing (input+output price asc) lookup.
type Cost struct{}

// compile-time cascade-lock assertion (Story 6.1 UNIT-022, BR2-1).
var _ engine.Strategy = Cost{}

// NewCost constructs the cost strategy stub.
func NewCost() Cost { return Cost{} }

// Select implements engine.Strategy.
func (Cost) Select(_ context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, error) {
	return firstAlphabetical(candidates)
}
