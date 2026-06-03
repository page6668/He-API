// Package handler implements the RoutingService.SelectModel Connect-RPC. It is
// a thin shaping layer over the decision engine: validate the request, call
// Engine.Decide, map the engine sentinels to gRPC codes (Q-F), and build the
// proto response with adapter_endpoint left reserved/empty (Q-K).
package handler

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1/routingv1connect"
)

// RoutingServer is the RoutingService handler. It is stateless beyond the
// engine + logger and safe for concurrent use (the engine is read-only).
type RoutingServer struct {
	engine *engine.Engine
	logger *slog.Logger
}

// compile-time assertion that we satisfy the generated handler interface.
var _ routingv1connect.RoutingServiceHandler = (*RoutingServer)(nil)

// NewRoutingServer builds the handler. logger may be nil — falls back to
// slog.Default().
func NewRoutingServer(e *engine.Engine, logger *slog.Logger) *RoutingServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &RoutingServer{engine: e, logger: logger}
}

// SelectModel resolves a routing decision.
//
// Error mapping (Q-F):
//   - empty/whitespace requested_model on the default/unspecified path -> InvalidArgument
//   - ErrNoCandidates (empty set / requested_model not in catalogue)   -> NotFound
//   - ErrUnknownStrategy (enum with no registered impl)                -> InvalidArgument
//
// Q-K: adapter_endpoint is never populated (the gateway resolves the endpoint
// via adapterclient.Registry). Q-I: strategy_used echoes the actually-fired
// strategy. The he_request_id is echoed to slog ONLY — never persisted, never
// returned. slog discipline is non-PII: strategy + model_id + he_request_id
// only (NEVER user_id — routing-svc never sees the JWT).
func (s *RoutingServer) SelectModel(
	ctx context.Context,
	req *connect.Request[routingv1.SelectModelRequest],
) (*connect.Response[routingv1.SelectModelResponse], error) {
	msg := req.Msg
	strat := msg.GetStrategy()

	// requested_model is required only on the default/unspecified path; the
	// named strategies select from the catalogue and ignore it in 6.1.
	if isDefaultPath(strat) && strings.TrimSpace(msg.GetRequestedModel()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("requested_model must not be empty"))
	}

	selected, used, scoreSource, err := s.engine.Decide(ctx, strat, engine.SelectionHints{
		UserID:         msg.GetUserId(),
		RequestedModel: msg.GetRequestedModel(),
		ABModels:       msg.GetAbModels(),
	})
	if err != nil {
		return nil, mapEngineError(err)
	}

	s.logger.InfoContext(
		ctx, "select_model",
		slog.String("event", "select_model"),
		slog.String("strategy", used.String()),
		slog.String("model_id", selected.ID),
		slog.String("score_source", scoreSource), // Story 6.2 High-1 (non-PII)
		slog.String("he_request_id", msg.GetHeRequestId()),
	)

	return connect.NewResponse(&routingv1.SelectModelResponse{
		SelectedModel: selected.ID,
		// AdapterEndpoint left empty — Q-K reserved-for-future.
		IsAbTest:         false,
		AbSelectedModels: nil,         // A/B is Story 6.4
		StrategyUsed:     used,        // Q-I: actually-fired strategy (truthful under degradation)
		ScoreSource:      scoreSource, // Story 6.2 High-1: model_pricing|clickhouse|fallback|default
	}), nil
}

// isDefaultPath reports whether the strategy resolves to the default
// (passthrough) path, where requested_model is mandatory. UNSPECIFIED(0) maps
// to DEFAULT (Q-D), so both require a requested_model.
func isDefaultPath(s routingv1.Strategy) bool {
	return s == routingv1.Strategy_STRATEGY_UNSPECIFIED || s == routingv1.Strategy_STRATEGY_DEFAULT
}

// mapEngineError translates an engine sentinel to the Q-F gRPC code.
func mapEngineError(err error) error {
	switch {
	case errors.Is(err, engine.ErrNoCandidates):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, engine.ErrUnknownStrategy):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		// Defensive: an unexpected error is an internal fault, not a client one.
		return connect.NewError(connect.CodeInternal, err)
	}
}
