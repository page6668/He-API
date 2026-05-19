// Story 4.7 — BR-1.10 warm-path performance regression marker.
//
// Both `/v1/models` and `/public/models` MUST serve under 5ms P95 on the
// warm path (in-memory catalogue; no DB hit). BR-1.10 is NOT an AC gate —
// it documents cold-start preservation so a future refactor (e.g., moving
// the snapshot to a DB-backed registry without a startup cache) surfaces
// a CI signal before merge.
//
// Scenario: 4.7-UNIT-013 — `testing.B` with a 1000-request loop on each
// endpoint. Assertion is a soft gate: failure emits `t.Log` rather than
// `t.Fatal` so CI noise on shared runners (Docker hosts under load) does
// not flap the suite. Dev / QA may upgrade to a hard gate if the metric
// stabilises.
package handlers

import (
	"context"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func BenchmarkModelsHandler_warm_path(b *testing.B) {
	h := NewModelsHandler(silentLogger())
	ctx := modelsAuthedCtx(context.Background())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
		h.ServeHTTP(rr, req)
	}
}

func BenchmarkPublicModelsHandler_warm_path(b *testing.B) {
	startedAt := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC).Unix()
	h := NewPublicModelsHandler(silentLogger(), BuildPublicModelsSnapshot(startedAt))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/public/models", nil)
		h.ServeHTTP(rr, req)
	}
}

// Scenario: 4.7-UNIT-013 (assertion mode)
// Priority: P1
//
// Direct P95 check — 1000 requests against each handler; sort latencies
// and compare the 950th sample to the 5ms ceiling. The check is gated as
// `t.Log`-on-fail rather than `t.Fatal` because CI runners under
// background load can transiently exceed the bound; the goal is to
// surface a SIGNAL on a real regression, not flap the suite.
func Test_models_endpoints_warm_path_under_5ms_P95(t *testing.T) {
	t.Parallel()
	const N = 1000
	const limit = 5 * time.Millisecond

	cases := []struct {
		name string
		hit  func()
	}{
		{
			name: "bearer-gated /v1/models",
			hit: func() {
				h := NewModelsHandler(silentLogger())
				ctx := modelsAuthedCtx(context.Background())
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/v1/models", nil).WithContext(ctx)
				h.ServeHTTP(rr, req)
			},
		},
		{
			name: "unauthenticated /public/models",
			hit: func() {
				startedAt := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC).Unix()
				h := NewPublicModelsHandler(silentLogger(), BuildPublicModelsSnapshot(startedAt))
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/public/models", nil)
				h.ServeHTTP(rr, req)
			},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			samples := make([]time.Duration, N)
			// Warm-up — discard first 10 to avoid cold-cache noise.
			for i := 0; i < 10; i++ {
				c.hit()
			}
			for i := 0; i < N; i++ {
				start := time.Now()
				c.hit()
				samples[i] = time.Since(start)
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			p95 := samples[int(float64(N)*0.95)]
			if p95 > limit {
				t.Logf("BR-1.10 regression marker: P95=%s exceeds %s (NOT an AC gate — signal only)", p95, limit)
			}
		})
	}
}
