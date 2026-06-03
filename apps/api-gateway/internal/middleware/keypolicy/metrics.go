// Story 5.2 T2.4 — OTel instruments for the key-policy enforcement gates.
//
// Realises the Q-H taxonomy via the codebase's OTel-meter idiom (parity with
// internal/middleware/ratelimit/metrics.go); the OTel→Prometheus exporter
// installed in cmd/server/main.go surfaces these as the Prometheus counter
// families named in Q-H. Cardinality is bounded — check ∈ {ip_whitelist,
// model_scope, monthly_cap}, result ∈ {allowed,denied,redis_error}, code ∈
// the three §5.1.2 envelopes. NO api_key_id / user_id label (unbounded).
package keypolicy

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// PolicyMetrics holds the three instrument families. nil-safe — every method
// no-ops on a nil receiver so tests can omit wiring.
type PolicyMetrics struct {
	checkTotal    metric.Int64Counter
	denialTotal   metric.Int64Counter
	checkDuration metric.Float64Histogram
}

// NewPolicyMetrics registers the instruments against the global meter
// provider. Returns a ready-to-use value (never nil).
func NewPolicyMetrics() *PolicyMetrics {
	m := otel.Meter("apps/api-gateway/internal/middleware/keypolicy")
	ct, _ := m.Int64Counter(
		"apikey_policy_check_total",
		metric.WithDescription("Key-policy checks by check and result (allowed/denied/redis_error)"),
	)
	dt, _ := m.Int64Counter(
		"apikey_policy_denial_total",
		metric.WithDescription("Key-policy denials by check and envelope code"),
	)
	cd, _ := m.Float64Histogram(
		"apikey_policy_check_duration_seconds",
		metric.WithDescription("Latency of the three sequential key-policy gates"),
	)
	return &PolicyMetrics{checkTotal: ct, denialTotal: dt, checkDuration: cd}
}

func (m *PolicyMetrics) check(ctx context.Context, checkName, result string) {
	if m == nil {
		return
	}
	m.checkTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("check", checkName),
		attribute.String("result", result),
	))
}

func (m *PolicyMetrics) denial(ctx context.Context, checkName, code string) {
	if m == nil {
		return
	}
	m.denialTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("check", checkName),
		attribute.String("code", code),
	))
}

func (m *PolicyMetrics) duration(ctx context.Context, seconds float64) {
	if m == nil {
		return
	}
	m.checkDuration.Record(ctx, seconds)
}
