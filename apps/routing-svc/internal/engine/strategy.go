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
// (ModelEntry, error)`. Implementations MUST be pure (no I/O beyond a read of
// an immutable snapshot, no mutation of candidates) and deterministic for a
// given input + snapshot.
type Strategy interface {
	Select(ctx context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, error)
}

// Score-source labels reported on SelectModelResponse.score_source (Story 6.2
// High-1). They are observability facts (slog + the he_routing_decisions_total
// metric), not part of the routing contract's behaviour.
const (
	// ScoreSourceModelPricing — the cost strategy ranked over he_api.model_pricing.
	ScoreSourceModelPricing = "model_pricing"
	// ScoreSourceClickHouse — a quality/latency strategy used real ClickHouse
	// scores (reachable once Epic 9 populates the backing store / under a
	// seeded test Scorer).
	ScoreSourceClickHouse = "clickhouse"
	// ScoreSourceFallback — a quality/latency strategy degraded to the
	// deterministic cost-then-alphabetical ordering because its Scorer had no
	// data (the default Epic-6 path, Q-A Option A).
	ScoreSourceFallback = "fallback"
	// ScoreSourceDefault — the passthrough (default) strategy; no scoring.
	ScoreSourceDefault = "default"
)

// SourcedStrategy is an OPTIONAL Story-6.2 capability layered on Strategy: a
// strategy that reports the score source (ScoreSource* above) it used for THIS
// decision. The data-availability of quality/latency is per-request, so the
// source cannot be a static property — it is reported alongside the selection.
//
// The cascade-locked Strategy.Select (BR2-1) is UNCHANGED: 6.3 (failover) / 6.4
// (A/B) strategies need only satisfy Strategy. Engine.Decide prefers
// SelectSourced when a strategy implements it, and reports ScoreSourceDefault
// for strategies that only satisfy Strategy.
type SourcedStrategy interface {
	Strategy
	SelectSourced(ctx context.Context, candidates []ModelEntry, hints SelectionHints) (ModelEntry, string, error)
}

// RankedStrategy is an OPTIONAL Story-6.3 capability layered on Strategy: a
// strategy that returns the FULL ranked candidate order (rank-1 first), not just
// the winner. The ranking feeds the gateway's failover_chain (Q-A Option A) —
// chain[0] is the selected_model, chain[1:] the ordered fallbacks.
//
// It mirrors the SourcedStrategy shape verbatim (BR1-2): like SelectSourced it
// also reports the score source, so a ranked decision is fully described by one
// call (the source is a property of the ranking, set once — Q-J). The
// cascade-locked Strategy.Select (BR2-1) is UNCHANGED: a plain Strategy (the
// DEFAULT passthrough) does NOT implement this and yields an empty failover tail
// (Q-D). Engine.Decide PREFERS SelectRanked when a strategy implements it.
//
// INVARIANT (BR1-4): SelectRanked is a pure lift of the winner-selection —
// chain[0] MUST equal the same strategy's Select/SelectSourced winner for the
// same input (zero regression). The he-router-* virtual entries are excluded
// from the WHOLE ranking (BR1-3), not just chain[0].
type RankedStrategy interface {
	Strategy
	SelectRanked(ctx context.Context, candidates []ModelEntry, hints SelectionHints) ([]ModelEntry, string, error)
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
