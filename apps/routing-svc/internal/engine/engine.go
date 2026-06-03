package engine

import (
	"context"
	"errors"
	"fmt"

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
// over the boot-loaded catalogue, and returns the selected model plus the
// strategy that ACTUALLY fired (strategy_used, Q-I).
//
//   - STRATEGY_UNSPECIFIED(0) is treated identically to STRATEGY_DEFAULT(1)
//     and never errors on the zero value (Q-D);
//   - an enum with no registered implementation -> ErrUnknownStrategy;
//   - the chosen Strategy.Select error (e.g. ErrNoCandidates) is propagated
//     verbatim for the handler to map to a gRPC code.
func (e *Engine) Decide(ctx context.Context, strategy routingv1.Strategy, hints SelectionHints) (modelscatalogue.ModelEntry, routingv1.Strategy, error) {
	effective := strategy
	if effective == routingv1.Strategy_STRATEGY_UNSPECIFIED {
		effective = routingv1.Strategy_STRATEGY_DEFAULT // Q-D zero-value rule
	}

	impl, ok := e.strategies[effective]
	if !ok {
		return modelscatalogue.ModelEntry{}, effective, ErrUnknownStrategy
	}

	selected, err := impl.Select(ctx, e.catalogue.List(), hints)
	if err != nil {
		return modelscatalogue.ModelEntry{}, effective, err
	}
	return selected, effective, nil
}
