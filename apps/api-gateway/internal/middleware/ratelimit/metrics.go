package ratelimit

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics holds the OTel instruments for the middleware. Registered once
// per process against the global meter provider (installed in
// cmd/server/main.go). Cardinality is bounded per BR-X.5:
//
//	he_ratelimit_decisions_total{axis,outcome}   = 3 × 2 = 6 series
//	he_ratelimit_fail_open_total{axis}           = 3 series
//	he_ratelimit_check_duration_seconds          = 1 histogram (no labels)
//
// NO api_key_id label anywhere — would unbound cardinality. NO content
// labels. Total NEW series budget ≤10.
type metrics struct {
	decisions     metric.Int64Counter
	failOpen      metric.Int64Counter
	checkDuration metric.Float64Histogram
	tpmDeductFail metric.Int64Counter
}

func newMetrics() *metrics {
	m := otel.Meter("apps/api-gateway/internal/middleware/ratelimit")
	dec, _ := m.Int64Counter(
		"he_ratelimit_decisions_total",
		metric.WithDescription("Rate-limit decisions by axis and outcome"),
	)
	fo, _ := m.Int64Counter(
		"he_ratelimit_fail_open_total",
		metric.WithDescription("Rate-limit fail-open events (Redis unavailable / timeout)"),
	)
	cd, _ := m.Float64Histogram(
		"he_ratelimit_check_duration_seconds",
		metric.WithDescription("Latency of the atomic check_and_incr Lua round-trip"),
	)
	td, _ := m.Int64Counter(
		"he_ratelimit_tpm_deduct_failed_total",
		metric.WithDescription("Post-deduction failures (Redis error after response sent)"),
	)
	return &metrics{decisions: dec, failOpen: fo, checkDuration: cd, tpmDeductFail: td}
}

func (m *metrics) decisionAllowed(ctx context.Context, axis string) {
	if m == nil {
		return
	}
	m.decisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("axis", axis),
		attribute.String("outcome", "allowed"),
	))
}

func (m *metrics) decisionDenied(ctx context.Context, axis string) {
	if m == nil {
		return
	}
	m.decisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String("axis", axis),
		attribute.String("outcome", "denied"),
	))
}

func (m *metrics) failOpenInc(ctx context.Context, axis string) {
	if m == nil {
		return
	}
	m.failOpen.Add(ctx, 1, metric.WithAttributes(attribute.String("axis", axis)))
}

func (m *metrics) recordDuration(ctx context.Context, seconds float64) {
	if m == nil {
		return
	}
	m.checkDuration.Record(ctx, seconds)
}

func (m *metrics) tpmDeductFailInc(ctx context.Context) {
	if m == nil {
		return
	}
	m.tpmDeductFail.Add(ctx, 1)
}
