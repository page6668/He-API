// meter_test — verifies that NewMeterProvider produces a MeterProvider
// whose counters round-trip through the Prometheus exporter into the
// `/metrics` HTTP handler. End-to-end: register a counter, increment it,
// scrape /metrics via WrapHTTPHandler, assert the line appears.

package obs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestNewMeterProvider_RoundTripsThroughMetricsEndpoint(t *testing.T) {
	// Install the meter provider globally so any package using
	// otel.GetMeterProvider().Meter(...) picks it up.
	mp, err := NewMeterProvider(context.Background(), "obs-test-svc", "obs-test-ns", "v0.0.0-test")
	if err != nil {
		t.Fatalf("NewMeterProvider: %v", err)
	}
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		otel.SetMeterProvider(prev)
	})

	// Register a counter and increment it.
	meter := otel.GetMeterProvider().Meter("obs/meter_test")
	counter, err := meter.Int64Counter("obs_test_counter_total")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 3)

	// Use WrapHTTPHandler to mount /metrics; this is the production
	// path so the test validates the seam end-to-end.
	handler := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}), "obs-test-svc")

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	page := string(body)

	// Prometheus exposition format uses underscored counter names; the
	// OTel→Prom bridge appends `_total` only if not already present.
	// Look for the counter name as a substring; the exact metric line
	// also carries the resource labels (service_name etc.) and the
	// value=3.
	if !strings.Contains(page, "obs_test_counter_total") {
		t.Errorf("expected counter name 'obs_test_counter_total' in /metrics, got:\n%s", page)
	}
	// Validate the value made it through.
	if !strings.Contains(page, "} 3") && !strings.Contains(page, "obs_test_counter_total 3") {
		t.Errorf("expected counter value 3 in /metrics, got:\n%s", page)
	}
}
