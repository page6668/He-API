package obs

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 1.5-UNIT-T5-007: full stack composes — NewTracerProvider + NewLogger +
// WrapHTTPHandler in a single happy path (mirrors how every scaffolded service
// wires obs in three calls). This is the contract Story 1.5 AC1.D depends on.
func TestObsStackComposes(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	logger := NewLogger(slog.LevelInfo)
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}

	tp, err := NewTracerProvider(context.Background(), "stack-test", "test-ns", "v0.0.1")
	if err != nil {
		t.Fatalf("NewTracerProvider: %v", err)
	}
	defer func() { _ = tp.Shutdown(context.Background()) }()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(WrapHTTPHandler(handler, "stack-test"))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}
