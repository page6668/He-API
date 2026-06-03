package strategy

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// Default is the passthrough strategy: it honours the caller's requested_model
// verbatim. This matches OpenAI-passthrough semantics for callers who name an
// explicit model. It is NOT a stub — its 6.1 behaviour is its final behaviour.
type Default struct{}

// compile-time cascade-lock assertion (Story 6.1 UNIT-022, BR2-1).
var _ engine.Strategy = Default{}

// NewDefault constructs the default (passthrough) strategy.
func NewDefault() Default { return Default{} }

// Select returns the candidate whose id equals hints.RequestedModel. It
// returns ErrNoCandidates when the set is empty OR when the requested model is
// not in the catalogue (the handler maps that to gRPC NotFound, Q-F).
func (Default) Select(_ context.Context, candidates []engine.ModelEntry, hints engine.SelectionHints) (engine.ModelEntry, error) {
	for _, c := range candidates {
		if c.ID == hints.RequestedModel {
			return c, nil
		}
	}
	return engine.ModelEntry{}, engine.ErrNoCandidates
}
