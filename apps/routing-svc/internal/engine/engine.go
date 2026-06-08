package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// Engine dispatches a SelectModel request to a registered Strategy, keyed by
// the proto Strategy enum. It is stateless and safe for concurrent use: the
// strategy map + catalogue are read-only after NewEngine (Story 6.1
// BLIND-CONCURRENCY-001).
type Engine struct {
	strategies map[routingv1.Strategy]Strategy
	catalogue  modelscatalogue.Catalogue
}

// NewEngine constructs the decision engine. It fail-fasts (AC1 boot
// validation) when:
//
//   - the catalogue is empty — readiness must never serve an empty catalogue
//     (BR1-2); the caller exits non-zero on this error;
//   - a strategy slug is registered with a nil implementation
//     (`routing-svc: strategy %q registered without implementation`).
//
// An out-of-range / unregistered enum is NOT rejected here — that surfaces at
// dispatch time as ErrUnknownStrategy (a defensive runtime sentinel, Q-F).
func NewEngine(catalogue modelscatalogue.Catalogue, strategies map[routingv1.Strategy]Strategy) (*Engine, error) {
	if catalogue.Len() == 0 {
		return nil, errors.New("routing-svc: models catalogue is empty")
	}
	for slug, impl := range strategies {
		if impl == nil {
			return nil, fmt.Errorf("routing-svc: strategy %q registered without implementation", slug)
		}
	}
	// Defensive copy of the map so a caller mutating its argument after
	// construction cannot race the read-only dispatch path.
	reg := make(map[routingv1.Strategy]Strategy, len(strategies))
	for slug, impl := range strategies {
		reg[slug] = impl
	}
	return &Engine{strategies: reg, catalogue: catalogue}, nil
}

// Decide resolves the effective strategy, dispatches to its implementation
// over the boot-loaded catalogue, and returns the selected model, the ordered
// failover tail (Story 6.3 — the ranked fallbacks AFTER the selected model,
// EXCLUDING it), the strategy that ACTUALLY fired (strategy_used, Q-I), and the
// score source (Story 6.2 High-1 — model_pricing|clickhouse|fallback|default).
//
//   - STRATEGY_UNSPECIFIED(0) is treated identically to STRATEGY_DEFAULT(1)
//     and never errors on the zero value (Q-D);
//   - an enum with no registered implementation -> ErrUnknownStrategy;
//   - the chosen Strategy.Select error (e.g. ErrNoCandidates) is propagated
//     verbatim for the handler to map to a gRPC code;
//   - a strategy implementing RankedStrategy (Story 6.3) yields the full ranked
//     order: selected = chain[0], failoverTail = chain[1:] (BR1-2);
//   - a strategy implementing SourcedStrategy (but not RankedStrategy) reports
//     its score source with an EMPTY failover tail;
//   - a plain Strategy reports ScoreSourceDefault with an EMPTY failover tail
//     (Q-D — the pinned/default path has no fallbacks).
func (e *Engine) Decide(ctx context.Context, strategy routingv1.Strategy, hints SelectionHints) (modelscatalogue.ModelEntry, []modelscatalogue.ModelEntry, routingv1.Strategy, string, error) {
	effective := strategy
	if effective == routingv1.Strategy_STRATEGY_UNSPECIFIED {
		effective = routingv1.Strategy_STRATEGY_DEFAULT // Q-D zero-value rule
	}

	impl, ok := e.strategies[effective]
	if !ok {
		return modelscatalogue.ModelEntry{}, nil, effective, "", ErrUnknownStrategy
	}

	// Story 6.3 — prefer the ranked capability so failover_chain is populated in
	// the SAME decision (Q-A Option A: zero extra round-trips on the failure
	// path). chain[0] is the selected model; chain[1:] is the ordered tail.
	if ranked, ok := impl.(RankedStrategy); ok {
		chain, source, err := ranked.SelectRanked(ctx, e.catalogue.List(), hints)
		if err != nil {
			return modelscatalogue.ModelEntry{}, nil, effective, "", err
		}
		if len(chain) == 0 {
			// Defensive: a non-error ranked result must hold at least the
			// winner; an empty chain is treated as no-route (never a hot-path
			// panic on chain[0]).
			return modelscatalogue.ModelEntry{}, nil, effective, "", ErrNoCandidates
		}
		return chain[0], chain[1:], effective, source, nil
	}

	if sourced, ok := impl.(SourcedStrategy); ok {
		selected, source, err := sourced.SelectSourced(ctx, e.catalogue.List(), hints)
		if err != nil {
			return modelscatalogue.ModelEntry{}, nil, effective, "", err
		}
		return selected, nil, effective, source, nil
	}

	selected, err := impl.Select(ctx, e.catalogue.List(), hints)
	if err != nil {
		return modelscatalogue.ModelEntry{}, nil, effective, "", err
	}
	return selected, nil, effective, ScoreSourceDefault, nil
}

// ResolveABModels validates the Story-6.4 A/B comparison legs against the boot
// catalogue (AC1/BR1-2). Each id MUST be a concrete catalogue model: present in
// the catalogue AND not a he-router-* virtual meta-entry (a meta id would miss
// adapterRegistry.Resolve on the gateway hot path — the correctness gate). It
// returns the validated ids in input order, or ErrABModelNotConcrete on the
// first leg that is unknown or a meta-model. It does NOT enforce the exactly-2
// count — the gateway owns the parse-time count check (BR1-4); the engine owns
// routability. It does not consult any Strategy: A/B overrides strategy
// selection (Q-I), so the legs ARE the selection.
func (e *Engine) ResolveABModels(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, metaModelPrefix) {
			return nil, ErrABModelNotConcrete
		}
		if _, ok := e.catalogue.Find(id); !ok {
			return nil, ErrABModelNotConcrete
		}
		out = append(out, id)
	}
	return out, nil
}
