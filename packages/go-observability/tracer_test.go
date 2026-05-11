package obs

import (
	"context"
	"testing"
	"time"
)

// 1.5-UNIT-T5-001: empty OTEL endpoint produces a no-export TracerProvider
// that does not panic on Shutdown (Q5 + AC1.D + BLIND-ERROR-003).
func TestNewTracerProvider_EmptyEndpointFallback(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	tp, err := NewTracerProvider(context.Background(), "test-svc", "test-ns", "v0.0.1")
	if err != nil {
		t.Fatalf("NewTracerProvider returned error in degraded mode: %v", err)
	}
	if tp == nil {
		t.Fatal("NewTracerProvider returned nil TracerProvider")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := tp.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown panicked / errored in degraded mode: %v", err)
	}
}

// 1.5-UNIT-T5-002: Shutdown is safe to call after creation (BLIND-RESOURCE).
func TestNewTracerProvider_ShutdownSafe(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tp, err := NewTracerProvider(context.Background(), "shutdown-svc", "test-ns", "v0.0.1")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	// Call Shutdown twice; the second call must not panic.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		t.Errorf("first Shutdown: %v", err)
	}
	if err := tp.Shutdown(ctx); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}
