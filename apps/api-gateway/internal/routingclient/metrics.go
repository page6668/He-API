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
type metrics struct {
	decisions metric.Int64Counter
	selectDur metric.Float64Histogram
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
	return &metrics{decisions: dec, selectDur: dur}
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
