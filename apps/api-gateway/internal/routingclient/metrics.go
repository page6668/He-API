package routingclient

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics holds the Story-6.2 routing instruments (Q-M). Registered once per
// process against the global meter provider (installed in cmd/server/main.go).
// Cardinality is bounded: `selected_model` ranges over the ~8 concrete
// catalogue models plus the small bypassed/none set (Q-M accepted); `strategy`
// is 5 values; `score_source` is 5 values. No api_key_id / user_id label.
//
//	he_routing_decisions_total{strategy,selected_model,score_source}
//	he_routing_select_duration_seconds  (histogram, no labels)
//
// Story 6.3 adds the failover instruments (Q-I), same bounded-cardinality
// discipline (from×to ranges over the ~8×8 catalogue; reason ∈ 2 values):
//
//	he_routing_failover_total{from_model,to_model,reason}
//	he_routing_failover_attempts  (histogram, integer buckets [1,2,3] — m-2)
type metrics struct {
	decisions        metric.Int64Counter
	selectDur        metric.Float64Histogram
	failoverTotal    metric.Int64Counter
	failoverAttempts metric.Int64Histogram
}

func newMetrics() *metrics {
	m := otel.Meter("apps/api-gateway/internal/routingclient")
	dec, _ := m.Int64Counter(
		"he_routing_decisions_total",
		metric.WithDescription("Routing decisions by strategy, selected model, and score source"),
	)
	dur, _ := m.Float64Histogram(
		"he_routing_select_duration_seconds",
		metric.WithDescription("Gateway-observed routing-svc SelectModel round-trip latency"),
	)
	failTotal, _ := m.Int64Counter(
		"he_routing_failover_total",
		metric.WithDescription("Automatic failover hops by from-model, to-model, and reason"),
	)
	// m-2 — an attempts-COUNT histogram bounded by MaxFailoverAttempts=3; the
	// default Prometheus latency buckets (.005…10s) are unreadable for a small
	// integer count, so pin integer-aligned boundaries [1,2,3].
	failAttempts, _ := m.Int64Histogram(
		"he_routing_failover_attempts",
		metric.WithDescription("Upstream attempts per chat request (1 = happy path, >1 = failover)"),
		metric.WithExplicitBucketBoundaries(1, 2, 3),
	)
	return &metrics{decisions: dec, selectDur: dur, failoverTotal: failTotal, failoverAttempts: failAttempts}
}

// failover records one failover hop (from_model → to_model) with the retriable
// reason (Q-I). reason ∈ {upstream_unavailable, upstream_timeout}.
func (m *metrics) failover(ctx context.Context, fromModel, toModel, reason string) {
	if m == nil {
		return
	}
	m.failoverTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("from_model", fromModel),
		attribute.String("to_model", toModel),
		attribute.String("reason", reason),
	))
}

// observeAttempts records the per-request upstream attempt count (1 on the happy
// path — BR4-2 zero-regression anchor).
func (m *metrics) observeAttempts(ctx context.Context, attempts int) {
	if m == nil {
		return
	}
	m.failoverAttempts.Record(ctx, int64(attempts))
}

func (m *metrics) decision(ctx context.Context, strategy, selectedModel, scoreSource string) {
	if m == nil {
		return
	}
	m.decisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("strategy", strategy),
		attribute.String("selected_model", selectedModel),
		attribute.String("score_source", scoreSource),
	))
}

func (m *metrics) recordDuration(ctx context.Context, seconds float64) {
	if m == nil {
		return
	}
	m.selectDur.Record(ctx, seconds)
}
