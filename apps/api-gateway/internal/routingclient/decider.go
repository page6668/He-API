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
	// IsAbTest (Story 6.4) is true when the request carried an X-He-AB-Models
	// header that resolved to a dual-leg A/B comparison (Q-I A/B-overrides). The
	// handler forks on it into dispatchAB; FALSE on every non-A/B path
	// (single-model 6.2/6.3 behaviour byte-for-byte).
	IsAbTest bool
	// AbSelectedModels (Story 6.4) carries the 2 resolved concrete leg ids on the
	// A/B path (empty otherwise). The legs ARE the selection — SelectedModel is
	// not meaningful on the A/B path.
	AbSelectedModels []string
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

// DecideAB resolves the Story-6.4 A/B decision (Q-I A/B-overrides-strategy). The
// gateway has already parsed X-He-AB-Models into exactly-2-distinct legs
// (ParseABModels); this populates SelectModelRequest.ab_models (BR1-1 — the
// 6.1-preshaped field 4, empty before 6.4) and reads back is_ab_test +
// ab_selected_models (the resolved concrete legs). `model` is the request's
// body.model, used ONLY to detect+WARN a strategy/meta-model specified alongside
// A/B (the model field + X-He-Routing-Strategy are ignored for selection).
//
// Error mapping: a leg validation failure (routing-svc InvalidArgument/NotFound —
// a he-router-* or unknown leg, BR1-2) -> 400_invalid_request envelope (Q-H). A
// transport fault (Unavailable/DeadlineExceeded/Internal) fails CLOSED -> 502:
// unlike a concrete single-model request (which fails OPEN to req.Model), A/B
// cannot dispatch unvalidated legs because the concrete-catalogue gate lives in
// routing-svc.
func (d *Decider) DecideAB(ctx context.Context, model string, abModels []string, header http.Header, userID, heRequestID string) (Decision, error) {
	// Q-I — WARN when a strategy directive was ALSO specified (A/B wins). A
	// directive is a he-router-* meta-model in `model` OR an X-He-Routing-Strategy
	// header.
	if strings.HasPrefix(model, MetaModelPrefix) || strings.TrimSpace(header.Get(RoutingStrategyHeader)) != "" {
		d.logger.WarnContext(ctx, "ab_overrides_strategy",
			slog.String("event", "ab_overrides_strategy"),
			slog.String("model", model),
			slog.String("header", header.Get(RoutingStrategyHeader)),
			slog.String("he_request_id", heRequestID),
		)
	}

	// Routing disabled -> passthrough A/B over the gateway-parsed concrete legs
	// (preserves the pre-6.2 nil-client behaviour; dispatch tests need no
	// routing-svc). The concrete-gate is then skipped — the caller supplied
	// concrete ids.
	if d.client == nil {
		return Decision{IsAbTest: true, AbSelectedModels: abModels, Bypassed: true}, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, d.deadline) // Q-E 100ms
	defer cancel()

	hdr := http.Header{}
	if heRequestID != "" {
		hdr.Set("X-He-Request-Id", heRequestID)
	}
	req := &routingv1.SelectModelRequest{
		UserId:      userID,
		AbModels:    abModels, // BR1-1 — POPULATE the 6.1-preshaped field 4
		HeRequestId: heRequestID,
	}

	start := time.Now()
	resp, err := d.client.SelectModel(callCtx, req, hdr)
	d.metrics.recordDuration(ctx, time.Since(start).Seconds())
	if err != nil {
		return d.mapABError(ctx, err, heRequestID)
	}
	legs := resp.GetAbSelectedModels()
	if !resp.GetIsAbTest() || len(legs) == 0 {
		// Defensive: routing-svc must echo the resolved legs on the A/B path.
		d.logNoRoute(ctx, model, heRequestID, "blank_ab_selected_models")
		return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
	}
	return Decision{IsAbTest: true, AbSelectedModels: legs}, nil
}

// mapABError maps a routing-svc SelectModel failure on the A/B path to a §5.1.2
// envelope (Q-H). A leg validation failure (InvalidArgument/NotFound) is a
// client error -> 400; any transport/availability fault fails CLOSED -> 502.
func (d *Decider) mapABError(ctx context.Context, err error, heRequestID string) (Decision, error) {
	switch connectCode(err) {
	case connect.CodeInvalidArgument, connect.CodeNotFound:
		return Decision{}, &EnvelopeError{Code: "400_invalid_request"}
	default:
		d.logNoRoute(ctx, "", heRequestID, "ab_routing_unavailable")
		return Decision{}, &EnvelopeError{Code: "502_upstream_unavailable"}
	}
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

// A/B outcome labels for he_routing_ab_total (Story 6.4 BR4-4 — bounded
// cardinality 3).
const (
	ABOutcomeBothOK     = "both_ok"
	ABOutcomePartial    = "partial"
	ABOutcomeBothFailed = "both_failed"
)

// RecordABOutcome emits the Story-6.4 he_routing_ab_total{outcome} instrument
// (AC4/BR4-4). It lives beside the routing/failover counters so all routing
// instruments share one registration point + bounded-cardinality discipline.
// Safe on a nil Decider (routing disabled → no-op).
func (d *Decider) RecordABOutcome(ctx context.Context, outcome string) {
	if d == nil {
		return
	}
	d.metrics.ab(ctx, outcome)
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
