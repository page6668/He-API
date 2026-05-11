// Package obs provides He-API's shared OpenTelemetry / Prometheus / slog
// wire-up for every Go service (Story 1.5, Architect Round 1 Q5 ruling).
//
// Three entry points:
//   - NewTracerProvider — OTLP/gRPC TracerProvider rooted at OTEL_EXPORTER_OTLP_ENDPOINT
//   - NewLogger          — slog JSON logger that stamps trace_id + span_id on every record
//   - WrapHTTPHandler    — otelhttp-wrapped mux that also registers /metrics for Prometheus
//
// The package extracts the patterns proven by Story 1.4's sample-otel-app so
// every Epic 2-10 gRPC service can opt into the full observability pyramid in
// ≤ 3 calls (Q5 abstraction contract).
package obs

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// NewTracerProvider returns an OTLP/gRPC TracerProvider whose resource is
// stamped with service.{name,namespace,version}. When OTEL_EXPORTER_OTLP_ENDPOINT
// is empty, the provider runs in degraded mode (valid SpanContexts still flow
// to the slog handler so trace_id-correlated log lines remain visible, but
// spans never reach the collector). This mirrors Story 1.4 1.4-UNIT-210.
func NewTracerProvider(ctx context.Context, name, namespace, version string) (*sdktrace.TracerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(name),
			semconv.ServiceNamespace(namespace),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("obs: resource init: %w", err)
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
		), nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("obs: otlp exporter init: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
	), nil
}
