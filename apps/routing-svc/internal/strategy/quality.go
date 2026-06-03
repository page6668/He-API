package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// Quality routes by model quality.
//
// STUB(6.1): returns the first-alphabetical-by-model-id candidate (Q-G). Story
// 6.2 replaces this with a benchmark_results.quality_score-desc lookup.
type Quality struct{}

// compile-time cascade-lock assertion (Story 6.1 UNIT-022, BR2-1).
var _ engine.Strategy = Quality{}

// NewQuality constructs the quality strategy stub.
func NewQuality() Quality { return Quality{} }

// Select implements engine.Strategy.
func (Quality) Select(_ context.Context, candidates []engine.ModelEntry, _ engine.SelectionHints) (engine.ModelEntry, error) {
	return firstAlphabetical(candidates)
}
