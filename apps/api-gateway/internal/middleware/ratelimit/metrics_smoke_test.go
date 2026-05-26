// Story 5.3 ISSUE-006 — verifies the OTel meter provider seam end-to-end:
// install obs.NewMeterProvider, drive the ratelimit middleware through one
// allowed + one denied + one fail-open + one TPMDeduct, then scrape the
// `/metrics` endpoint and assert all four he_ratelimit_* metric families
// appear. Guards against future regressions where main.go forgets to call
// otel.SetMeterProvider BEFORE constructing the middleware.

package ratelimit_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
	obs "github.com/he-api/he-api/packages/go-observability"
)

// TestMetricsEmitOnMetricsEndpoint asserts the 4 OTel instruments the
// ratelimit package registers (decisions_total, fail_open_total,
// check_duration_seconds, tpm_deduct_failed_total) actually appear on the
// Prometheus `/metrics` exposition after the meter provider is installed.
func TestMetricsEmitOnMetricsEndpoint(t *testing.T) {
	// Install meter provider FIRST — ratelimit.New constructs instruments
	// against otel.Meter(...) which reads the global provider. If we set
	// it after construction, the counters get orphaned against the no-op
	// (the exact bug Dev's Round 1 Risk Note line 138-144 flagged).
	mp, err := obs.NewMeterProvider(context.Background(), "ratelimit-metrics-test", "he-api-test", "v0.0.0-test")
	if err != nil {
		t.Fatalf("NewMeterProvider: %v", err)
	}
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := ratelimit.New(ratelimit.Config{
		Redis:            client,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 1, RPMMax: 5, TPMMax: 1000},
		FailOpenTimeout:  100 * time.Millisecond,
	}, logger)

	var called atomic.Int32
	handler := m.Wrap(passHandler(&called))

	// (1) allowed — increments decisions_total{outcome=allowed}.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, withAPIKeyID("k-metrics"))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", rec1.Code)
	}

	// (2) denied — second QPS request inside same second exhausts.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, withAPIKeyID("k-metrics"))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: want 429, got %d", rec2.Code)
	}

	// (3) fail-open — point client at a dead address; m.cfg.Redis is the
	// same client but we stop miniredis to force a network error within
	// FailOpenTimeout. Use a fresh middleware with nil Redis (cheaper +
	// deterministic — exercises the ErrRedisUnavailable branch).
	failOpenMW := ratelimit.New(ratelimit.Config{
		Redis:            nil,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 1, RPMMax: 1, TPMMax: 1},
		FailOpenTimeout:  100 * time.Millisecond,
	}, logger)
	failHandler := failOpenMW.Wrap(passHandler(&called))
	rec3 := httptest.NewRecorder()
	failHandler.ServeHTTP(rec3, withAPIKeyID("k-fail-open-metrics"))
	if rec3.Code != http.StatusOK {
		t.Fatalf("fail-open: want 200, got %d", rec3.Code)
	}

	// (4) TPMDeduct — exercises check_duration_seconds (via the .check
	// path above) and tpm_deduct_failed_total path (force a failure by
	// closing miniredis before calling TPMDeduct).
	m.TPMDeduct(context.Background(), "k-metrics", 42)
	// Force a deduct failure to populate tpm_deduct_failed_total.
	mr.Close()
	m.TPMDeduct(context.Background(), "k-metrics", 7)

	// Scrape /metrics via the same WrapHTTPHandler the production server
	// uses — validates the end-to-end seam.
	scrapeMux := obs.WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), "ratelimit-metrics-test")
	srv := httptest.NewServer(scrapeMux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	page := string(body)

	wantFamilies := []string{
		"he_ratelimit_decisions_total",
		"he_ratelimit_fail_open_total",
		"he_ratelimit_check_duration_seconds",
		"he_ratelimit_tpm_deduct_failed_total",
	}
	for _, want := range wantFamilies {
		if !strings.Contains(page, want) {
			t.Errorf("expected metric family %q on /metrics; full body:\n%s", want, page)
		}
	}
}
