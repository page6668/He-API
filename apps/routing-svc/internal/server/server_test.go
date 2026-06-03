// Server assembly + health-probe tests (Story 6.1 AC1).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-001 (server level)  empty catalogue -> New returns construction error (BR1-2)
//	6.1-BLIND-ERROR-001          nil/empty registry -> boot fail-fast (dependency seam)
//	6.1-INT-002                  GET /livez -> 200 unconditionally
//	6.1-INT-003 / 6.1-BLIND-FLOW-001  GET /ready -> 503 before ready, 200 after; engine
//	                             constructed before readiness can flip
//	6.1-INT-004                  /metrics served (observability REUSED)
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		Catalogue:  modelscatalogue.DefaultCatalogue,
		Strategies: strategy.DefaultStrategies(strategy.Deps{}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// 6.1-UNIT-001 + 6.1-BLIND-ERROR-001 — empty catalogue is a fatal boot error.
func Test_New_empty_catalogue_fails_fast(t *testing.T) {
	_, err := New(Options{
		Catalogue:  modelscatalogue.NewFromRegistry(modelscatalogue.Registry{}),
		Strategies: strategy.DefaultStrategies(strategy.Deps{}),
	})
	if err == nil {
		t.Fatal("New accepted an empty catalogue; want fail-fast error (BR1-2)")
	}
}

// 6.1-INT-002 — /livez is 200 even before readiness flips.
func Test_livez_always_200(t *testing.T) {
	srv := newTestServer(t) // ready == false
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/livez")
	if err != nil {
		t.Fatalf("GET /livez: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/livez status = %d, want 200 (unconditional)", resp.StatusCode)
	}
}

// 6.1-INT-003 + 6.1-BLIND-FLOW-001 — /ready is 503 before SetReady, 200 after.
func Test_ready_503_then_200(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Before readiness flips.
	resp, err := http.Get(ts.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/ready (pre-ready) status = %d, want 503 (BR1-2 gate)", resp.StatusCode)
	}

	// After the listener is up + engine constructed.
	srv.SetReady(true)
	resp2, err := http.Get(ts.URL + "/ready")
	if err != nil {
		t.Fatalf("GET /ready: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("/ready (post-ready) status = %d, want 200", resp2.StatusCode)
	}
}

// 6.1-INT-004 — /metrics is served by the REUSED observability middleware
// (no bespoke handler wired in this package).
func Test_metrics_served_by_obs_wrapper(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/metrics status = %d, want 200 (promhttp via WrapHTTPHandler)", resp.StatusCode)
	}
}
