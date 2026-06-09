package fx

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the Story-7.2 fx-refresh instruments (T2.4 / INT-031).
// Registered once per process against the global meter (installed in the
// fx-refresh cmd main). Cardinality is bounded: `reason` ∈ {provider_error,
// non_positive_rate, db_error} (3 values). The last-success timestamp is an
// Int64ObservableGauge over an atomic (otel/metric v1.26.0 has no synchronous
// Int64Gauge — Architect [[project_otel_version_pin_gotcha]] pin).
//
//	he_billing_fx_refresh_completed_total
//	he_billing_fx_refresh_failed_total{reason}
//	he_billing_fx_refresh_last_success_timestamp_seconds (gauge)
type Metrics struct {
	completed   metric.Int64Counter
	failed      metric.Int64Counter
	lastSuccess atomic.Int64 // unix seconds of the last successful refresh
}

// NewMetrics registers the fx-refresh instruments against the global meter.
func NewMetrics() *Metrics {
	m := otel.Meter("apps/billing-svc/internal/fx")
	completed, _ := m.Int64Counter(
		"he_billing_fx_refresh_completed_total",
		metric.WithDescription("Successful fx-refresh runs (a fresh USD→CNY row appended)"),
	)
	failed, _ := m.Int64Counter(
		"he_billing_fx_refresh_failed_total",
		metric.WithDescription("Failed/stale-serve fx-refresh runs by reason (provider_error/non_positive_rate/db_error)"),
	)
	out := &Metrics{completed: completed, failed: failed}
	_, _ = m.Int64ObservableGauge(
		"he_billing_fx_refresh_last_success_timestamp_seconds",
		metric.WithDescription("Unix timestamp of the last successful fx-refresh (staleness watchdog)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			if v := out.lastSuccess.Load(); v > 0 {
				o.Observe(v)
			}
			return nil
		}),
	)
	return out
}

func (m *Metrics) completedInc(ctx context.Context) {
	if m == nil || m.completed == nil {
		return
	}
	m.completed.Add(ctx, 1)
}

func (m *Metrics) failedInc(ctx context.Context, reason string) {
	if m == nil || m.failed == nil {
		return
	}
	m.failed.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *Metrics) recordSuccessAt(t time.Time) {
	if m == nil {
		return
	}
	m.lastSuccess.Store(t.Unix())
}
