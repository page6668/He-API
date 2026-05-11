package obs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 1.5-UNIT-T5-005: WrapHTTPHandler exposes Prometheus metrics on /metrics
// with HTTP 200 + recognisable Prometheus text format.
func TestWrapHTTPHandler_MetricsEndpoint(t *testing.T) {
	wrapped := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), "test-svc")

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "# HELP") && !strings.Contains(string(body), "# TYPE") {
		t.Errorf("response does not look like Prometheus text exposition; got %.200q", body)
	}
}

// 1.5-UNIT-T5-006: WrapHTTPHandler delegates non-/metrics paths to the inner
// handler unchanged (validates otelhttp wrap composition).
func TestWrapHTTPHandler_DelegatesOtherPaths(t *testing.T) {
	wrapped := WrapHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("delegated"))
	}), "test-svc")

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "delegated" {
		t.Fatalf("inner handler not invoked; body=%q", body)
	}
}
