package routingclient

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// DefaultDeadline is the Q-E SelectModel budget applied at the gateway call site
// (the Story-6.1 lifetime contract value).
const DefaultDeadline = 100 * time.Millisecond

// score_source labels the gateway derives locally for the non-routing-svc paths
// (the routing-svc-sourced values — model_pricing|clickhouse|fallback|default —
// arrive on the wire and are echoed verbatim).
const (
	scoreSourceDefault  = "default"  // routing disabled (no client) → passthrough
	scoreSourceBypassed = "bypassed" // fail-open passthrough (routing-svc unavailable)
)

// Decision is the resolved routing outcome the handler acts on: the model to
// resolve + surface on X-He-Selected-Model, the strategy that fired, the score
// source (for slog + the metric), and whether the decision was a fail-open
// passthrough (Q-G).
type Decision struct {
	SelectedModel string
	Strategy      routingv1.Strategy
	ScoreSource   string
	Bypassed      bool
	// FailoverChain (Story 6.3) is the ordered tail of fallback model ids AFTER
	// SelectedModel (rank-2, rank-3, …), concrete-only (the he-router-* virtual
	// entries are excluded by routing-svc — BR1-3). It is EMPTY on the
	// concrete-default/passthrough + fail-open paths (Q-D — a pinned model has no
	// fallbacks) and on a single-candidate catalogue. The handler scope-filters
	// it (Q-K) and iterates it on a retriable 502/504 (Q-A Option A).
	FailoverChain []string
}

// EnvelopeError signals the handler to write an OpenAI §5.1.2 error envelope
// with the carried code (via the canonical openaierr.Write — BR4-3). It is the
// fail-closed / invalid-request outcome of a decision (Q-H).
type EnvelopeError struct {
	Code string // openaierr code, e.g. 400_invalid_request, 502_upstream_unavailable
}

func (e *EnvelopeError) Error() string { return "routing: " + e.Code }

// Decider runs the full hot-path routing decision. Construct once at startup
// and reuse; safe for concurrent use (the ClientHandle + metrics are immutable).
type Decider struct {
	client   ClientHandle
	deadline time.Duration
	metrics  *metrics
	logger   *slog.Logger
}

// NewDecider builds a Decider. A nil client disables routing — Decide then
// returns a req.Model passthrough Decision (today's behaviour). logger may be
// nil (slog.Default()).
func NewDecider(client ClientHandle, logger *slog.Logger) *Decider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Decider{
		client:   client,
		deadline: DefaultDeadline,
		metrics:  newMetrics(),
		logger:   logger,
	}
}

// Decide resolves the routing decision for a chat request. It returns either a
// Decision (success or fail-open passthrough) or an *EnvelopeError (fail-closed
// / invalid). The 100ms deadline (Q-E) is applied at this call site.
func (d *Decider) Decide(ctx context.Context, model string, header http.Header, userID, heRequestID string) (Decision, error) {
	strategy, requested, isMeta, conflict := ParseStrategy(model, header)
	if conflict {
		d.logger.WarnContext(ctx, "strategy_conflict",
			slog.String("event", "strategy_conflict"),
			slog.String("model", model),
			slog.String("header", header.Get(RoutingStrategyHeader)),
			slog.String("resolved_strategy", strategyLabel(strategy)),
			slog.String("he_request_id", heRequestID),
		) // Q-I: meta-model wins; header ignored.
	}

	// Routing disabled → passthrough (preserves pre-6.2 behaviour).
	if d.client == nil {
		return Decision{SelectedModel: model, Strategy: strategy, ScoreSource: scoreSourceDefault, Bypassed: true}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, d.deadline) // Q-E 100ms at the call site
	defer cancel()

	hdr := http.Header{}
	if heRequestID != "" {
		hdr.Set("X-He-Request-Id", heRequestID)
	}
	req := &routingv1.SelectModelRequest{
		UserId:         userID,
		RequestedModel: requested,
		Strategy:       strategy,
		HeRequestId:    heRequestID,
		// AbModels intentionally empty — A/B is Story 6.4 (Q-N / BR1-4).
	}

	start := time.Now()
	resp, err := d.client.SelectModel(callCtx, req, hdr)
	d.metrics.recordDuration(ctx, time.Since(start).Seconds())

	if err != nil {
		return d.mapError(ctx, err, model, strategy, isMeta, heRequestID)
	}

	selected := strings.TrimSpace(resp.GetSelectedModel())
	if selected == "" {
		// BLIND-ERROR-002 — a blank selected_model is treated as no-route, never
		// a silent empty header.
		return d.noRoute(ctx, model, isMeta, heRequestID, "blank_selected_model")
	}

	dec := Decision{
		SelectedModel: selected,
		Strategy:      resp.GetStrategyUsed(),
		ScoreSource:   resp.GetScoreSource(),
		FailoverChain: resp.GetFailoverChain(), // Story 6.3 — ranked fallback tail (Q-A Option A)
	}
	d.metrics.decision(ctx, strategyLabel(dec.Strategy), dec.SelectedModel, dec.ScoreSource)
	return dec, nil
}

// FilterByScope returns the chain entries the key is authorised for, preserving
// order (Story 6.3 BR2-5 / Q-K). An empty scope means "all models allowed"
// (Story 5.2 keypolicy BR-3.1), so the chain is returned unchanged. Matching is
// case-sensitive — parity with the keypolicy.CheckModelScope canonical form. The
// gateway applies this to the failover tail BEFORE dispatch so failover NEVER
// targets a scoped-out model (chain[0]/selected_model already passed the 6.2
// gate). A nil/empty result is a valid terminal (every fallback scoped out).
func FilterByScope(chain, scopeModels []string) []string {
	if len(scopeModels) == 0 {
		return chain
	}
	allowed := make(map[string]struct{}, len(scopeModels))
	for _, m := range scopeModels {
		allowed[m] = struct{}{}
	}
	out := make([]string, 0, len(chain))
	for _, m := range chain {
		if _, ok := allowed[m]; ok {
			out = append(out, m)
		}
	}
	return out
}

// RecordFailover emits the Story-6.3 failover hop instrument + slog is handled
// by the handler (which owns the per-attempt context). It is a thin pass-through
// to the routingclient metrics so the failover counter lives beside the routing
// decision counter (bounded cardinality, one registration point). Safe on a nil
// Decider (routing disabled → no-op).
func (d *Decider) RecordFailover(ctx context.Context, fromModel, toModel, reason string) {
	if d == nil {
		return
	}
	d.metrics.failover(ctx, fromModel, toModel, reason)
}

// RecordFailoverAttempts observes the per-request upstream attempt count (Q-I /
// BR4-2 — 1 on the happy path). Safe on a nil Decider.
func (d *Decider) RecordFailoverAttempts(ctx context.Context, attempts int) {
	if d == nil {
		return
	}
	d.metrics.observeAttempts(ctx, attempts)
}

// mapError implements the Q-H gRPC→§5.1.2 mapping + the Q-G fail-open/closed
// fork by requested_model shape (isMeta).
func (d *Decider) mapError(ctx context.Context, err error, model string, strategy routingv1.Strategy, isMeta bool, heRequestID string) (Decision, error) {
	switch connectCode(err) {
	case connect.CodeInvalidArgument:
		// e.g. empty requested_model on the default path — a client error.
		return Decision{}, &EnvelopeError{Code: "400_invalid_request"}

	case connect.CodeNotFound:
		// ErrNoCandidates: meta-model → no routable model (502); concrete-default
		// miss → model-not-found (400). (Q-H)
		if isMeta {
			d.logNoRoute(ctx, model, heRequestID, "no_route")
			return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
		}
		return Decision{}, &EnvelopeError{Code: "400_invalid_request"}

	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		// Q-G: meta-model → fail-CLOSED (no concrete fallback); concrete → fail-OPEN.
		if isMeta {
			d.logNoRoute(ctx, model, heRequestID, "bypassed_unavailable_meta")
			return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
		}
		return d.failOpen(ctx, model, strategy, heRequestID), nil

	default:
		// Internal / Unknown / transport — degrade like Unavailable (Q-G shape).
		if isMeta {
			d.logNoRoute(ctx, model, heRequestID, "bypassed_error_meta")
			return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
		}
		return d.failOpen(ctx, model, strategy, heRequestID), nil
	}
}

// failOpen returns a passthrough Decision for a concrete model when routing-svc
// is unavailable (Q-G) — chat keeps working; the header carries req.Model.
func (d *Decider) failOpen(ctx context.Context, model string, strategy routingv1.Strategy, heRequestID string) Decision {
	d.logger.WarnContext(ctx, "routing_bypassed_unavailable",
		slog.String("event", "routing_bypassed_unavailable"),
		slog.String("routing", "bypassed_unavailable"),
		slog.String("model", model),
		slog.String("he_request_id", heRequestID),
	)
	dec := Decision{SelectedModel: model, Strategy: strategy, ScoreSource: scoreSourceBypassed, Bypassed: true}
	d.metrics.decision(ctx, strategyLabel(strategy), model, scoreSourceBypassed)
	return dec
}

// noRoute handles a successful-but-unusable response (blank selected_model):
// meta → 502 (no fallback); concrete → fail-open passthrough.
func (d *Decider) noRoute(ctx context.Context, model string, isMeta bool, heRequestID, reason string) (Decision, error) {
	if isMeta {
		d.logNoRoute(ctx, model, heRequestID, reason)
		return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
	}
	return d.failOpen(ctx, model, routingv1.Strategy_STRATEGY_DEFAULT, heRequestID), nil
}

func (d *Decider) logNoRoute(ctx context.Context, model, heRequestID, reason string) {
	d.logger.WarnContext(ctx, "routing_no_route",
		slog.String("event", "routing_no_route"),
		slog.String("reason", reason),
		slog.String("model", model),
		slog.String("he_request_id", heRequestID),
	)
}

// connectCode extracts the Connect code from err, normalising a bare
// context.DeadlineExceeded (the 100ms budget) to CodeDeadlineExceeded.
func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.CodeDeadlineExceeded
	}
	return connect.CodeUnknown
}
