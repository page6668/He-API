// Package engine is the routing-svc decision core: the cascade-locked
// Strategy interface (Story 6.1 BR2-1), the SelectionHints input, the
// exported sentinel errors, and the dispatch Engine that maps a request's
// Strategy enum to a registered implementation.
//
// Story 6.1 ships the interface + a deterministic-stub realisation; Stories
// 6.2 (real scoring) / 6.3 (failover) / 6.4 (A/B) / 6.5 (UI) bind to this
// exact shape. DO NOT change the Strategy signature without a coordinated
// cascade across those stories.
package engine

import (
	"context"
	"errors"

	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

// ModelEntry is the candidate type a Strategy ranks over. It is an alias of
// the shared catalogue entry so the single source of truth (Story 6.1 Q-A)
// flows unchanged into the routing core — there is no parallel model type.
type ModelEntry = modelscatalogue.ModelEntry

// SelectionHints carries the per-request inputs a Strategy may consult beyond
// the candidate set. The shape is cascade-locked (BR2-1): 6.2+ strategies read
// the same fields. ABModels is reserved for Story 6.4 (A/B) and ignored by the
// 6.1 stubs.
type SelectionHints struct {
	UserID         string
	RequestedModel string
	ABModels       []string
}

// Strategy selects exactly one model from the candidate set. The signature is
// the Story 6.1 cascade-lock (BR2-1) — `Select(ctx, []ModelEntry, hints) ->
// (ModelEntry, error)`. Implementations MUST be pure (no I/O, no mutation of
// candidates) and deterministic for a given input.
type Strategy interface {
	Select(ctx context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, error)
}

// Exported sentinels (BR2-4) — compared with errors.Is by the handler to map
// to gRPC codes (Q-F): ErrNoCandidates -> NotFound, ErrUnknownStrategy ->
// InvalidArgument.
var (
	// ErrNoCandidates is returned when the candidate set is empty, or when the
	// default strategy's requested_model is not present in the catalogue.
	ErrNoCandidates = errors.New("routing: no candidate models")
	// ErrUnknownStrategy is a defensive internal sentinel: a strategy enum
	// value that has no registered implementation. Post-Q-D it is NOT a normal
	// gateway path (UNSPECIFIED maps to DEFAULT), only a misconfiguration or an
	// out-of-range enum value.
	ErrUnknownStrategy = errors.New("routing: unknown strategy")
)
