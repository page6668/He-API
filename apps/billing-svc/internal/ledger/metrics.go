package ledger

import (
	"context"

	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the Story-7.1 billing instruments (T5.4). Registered once per
// process against the global meter provider (installed in cmd/server/main.go).
// Cardinality is bounded: `result` ∈ {applied, duplicate, no_pricing, error}
// (4 values); `target` ∈ {balance_realtime, month_cost_counter,
// api_key_month_cost} (3 values). No user_id / api_key_id / he_request_id label
// (those are slog-only — the metrics stay low-cardinality).
//
//	he_billing_deductions_total{result}
//	he_billing_deduct_amount_usd          (histogram, USD)
//	he_billing_redis_mirror_failed_total{target}
type Metrics struct {
	deductions   metric.Int64Counter
	deductAmount metric.Float64Histogram
	mirrorFailed metric.Int64Counter
}

// NewMetrics registers the billing instruments against the global meter.
func NewMetrics() *Metrics {
	m := otel.Meter("apps/billing-svc/internal/ledger")
	deductions, _ := m.Int64Counter(
		"he_billing_deductions_total",
		metric.WithDescription("Balance deductions by result (applied/duplicate/no_pricing/error)"),
	)
	amount, _ := m.Float64Histogram(
		"he_billing_deduct_amount_usd",
		metric.WithDescription("Per-request deducted cost in USD"),
	)
	mirrorFailed, _ := m.Int64Counter(
		"he_billing_redis_mirror_failed_total",
		metric.WithDescription("Post-commit cross-store mirror failures by target (PG charge still durable)"),
	)
	return &Metrics{deductions: deductions, deductAmount: amount, mirrorFailed: mirrorFailed}
}

func (m *Metrics) deductionInc(ctx context.Context, result string) {
	if m == nil || m.deductions == nil {
		return
	}
	m.deductions.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

func (m *Metrics) observeAmount(ctx context.Context, cost decimal.Decimal) {
	if m == nil || m.deductAmount == nil {
		return
	}
	m.deductAmount.Record(ctx, cost.InexactFloat64())
}

func (m *Metrics) mirrorFailedInc(ctx context.Context, target string) {
	if m == nil || m.mirrorFailed == nil {
		return
	}
	m.mirrorFailed.Add(ctx, 1, metric.WithAttributes(attribute.String("target", target)))
}
