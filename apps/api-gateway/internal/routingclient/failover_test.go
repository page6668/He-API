// Story 6.3 — routingclient failover plumbing: scope filter + metrics.
//
// Scenario trace -> docs/qa/assessments/6.3-test-design-20260603.md:
//
//	6.3-UNIT-030  FilterByScope drops scoped-out models BEFORE dispatch (Q-K)
//	6.3-UNIT-031  scope filter empties the chain -> terminal (boundary)
//	6.3-UNIT-043  he_routing_failover_total{from,to,reason} +1 per hop
//	6.3-UNIT-044  he_routing_failover_attempts uses integer buckets [1,2,3] (m-2)
//	6.3-UNIT-047  metrics reuse the routingclient pattern; bounded cardinality
package routingclient_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
)

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 6.3-UNIT-030 (P0) — FilterByScope keeps only authorised models, in order.
func Test_UNIT_030_FilterByScope_DropsScopedOut(t *testing.T) {
	chain := []string{"a", "b", "c", "d"}
	got := routingclient.FilterByScope(chain, []string{"a", "c"})
	if !eqStr(got, []string{"a", "c"}) {
		t.Errorf("FilterByScope = %v, want [a c]", got)
	}
}

// 6.3-UNIT-030b — an EMPTY scope means "all models allowed" (Story 5.2 BR-3.1):
// the chain passes through unchanged.
func Test_UNIT_030_FilterByScope_EmptyScopeAllowsAll(t *testing.T) {
	chain := []string{"a", "b"}
	got := routingclient.FilterByScope(chain, nil)
	if !eqStr(got, chain) {
		t.Errorf("FilterByScope(empty scope) = %v, want %v (all allowed)", got, chain)
	}
}

// 6.3-UNIT-031 (P1) — when scope filtering removes every fallback, the result is
// empty (the loop then terminates on the last upstream error — no panic).
func Test_UNIT_031_FilterByScope_EmptiesChain(t *testing.T) {
	got := routingclient.FilterByScope([]string{"b", "c"}, []string{"x", "y"})
	if len(got) != 0 {
		t.Errorf("FilterByScope = %v, want empty", got)
	}
}

// 6.3-UNIT-043/044/047 (P1) — the failover metrics record with the right shape:
// a counter +1 per hop and a histogram with integer-aligned buckets [1,2,3].
func Test_UNIT_043_FailoverMetrics_RecordedWithBuckets(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { otel.SetMeterProvider(otel.GetMeterProvider()) })

	// NewDecider(nil, nil) registers the instruments against the global provider.
	d := routingclient.NewDecider(nil, nil)
	ctx := context.Background()
	d.RecordFailover(ctx, "m1", "m2", "upstream_unavailable")
	d.RecordFailover(ctx, "m2", "m3", "upstream_timeout")
	d.RecordFailoverAttempts(ctx, 3)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var sawTotal, sawAttempts bool
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "he_routing_failover_total":
				sawTotal = true
				sum, ok := m.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("failover_total is %T, want Sum[int64]", m.Data)
				}
				var total int64
				for _, dp := range sum.DataPoints {
					total += dp.Value
				}
				if total != 2 {
					t.Errorf("failover_total sum = %d, want 2 (one per hop)", total)
				}
			case "he_routing_failover_attempts":
				sawAttempts = true
				hist, ok := m.Data.(metricdata.Histogram[int64])
				if !ok {
					t.Fatalf("failover_attempts is %T, want Histogram[int64]", m.Data)
				}
				if len(hist.DataPoints) == 0 {
					t.Fatalf("no histogram data points")
				}
				// m-2 — integer-aligned buckets [1,2,3], NOT default latency buckets.
				if got := hist.DataPoints[0].Bounds; !eqF(got, []float64{1, 2, 3}) {
					t.Errorf("attempts histogram bounds = %v, want [1 2 3] (m-2)", got)
				}
			}
		}
	}
	if !sawTotal || !sawAttempts {
		t.Errorf("missing instruments: total=%v attempts=%v", sawTotal, sawAttempts)
	}
}

func eqF(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
