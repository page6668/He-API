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
	// Story 5.4 — sticky-trip fast-path hits + threshold-notify fires.
	sentinelHitTotal metric.Int64Counter
	notifyTotal      metric.Int64Counter
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
	sh, _ := m.Int64Counter(
		"he_apikey_cap_tripped_sentinel_hit_total",
		metric.WithDescription("Sticky-trip sentinel fast-path hits (402 without a counter GET)"),
	)
	nt, _ := m.Int64Counter(
		"he_apikey_cap_threshold_notify_total",
		metric.WithDescription("Cap threshold-crossing notifications fired, by threshold + outcome"),
	)
	return &PolicyMetrics{checkTotal: ct, denialTotal: dt, checkDuration: cd, sentinelHitTotal: sh, notifyTotal: nt}
}

// sentinelHit records a sticky-trip fast-path 402 (BR-1.5).
func (m *PolicyMetrics) sentinelHit(ctx context.Context) {
	if m == nil {
		return
	}
	m.sentinelHitTotal.Add(ctx, 1)
}

// notify records a threshold-crossing fire (outcome is "fired" gateway-side;
// notification-svc owns the dedup/error outcomes).
func (m *PolicyMetrics) notify(ctx context.Context, threshold, outcome string) {
	if m == nil {
		return
	}
	m.notifyTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("threshold", threshold),
		attribute.String("outcome", outcome),
	))
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
