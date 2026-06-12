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
	"log/slog"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// samplerFromEnv builds the head sampler from OTEL_TRACES_SAMPLER_ARG (Story 9.4
// BR-TR-5, Q-SAMPLE ratified). It is ALWAYS ParentBased(TraceIDRatioBased(ratio))
// so a child service NEVER makes an independent sampling decision — a request the
// gateway (the sampling root) sampled stays sampled across the whole chain, and an
// unsampled one is dropped consistently (no half-traces / no holes).
//
// ratio is read from OTEL_TRACES_SAMPLER_ARG as a float in [0,1]; anything
// unparseable or out of range falls back to 1.0 with a startup warn — never a
// panic (data-validation: 9.4-UNIT-007, BLIND-BOUNDARY-002). Per-env values are
// fixed by the Architect: non-prod = 1.0, prod = 0.1 (recorded in
// infrastructure-deployment). Errors-always-sampled is NOT a head concern (the
// error hasn't happened at root-span time) — it lives in the collector tail
// sampler (Q-COLLECTOR).
func samplerFromEnv() sdktrace.Sampler {
	ratio := 1.0
	if arg := os.Getenv("OTEL_TRACES_SAMPLER_ARG"); arg != "" {
		if v, err := strconv.ParseFloat(arg, 64); err == nil && v >= 0 && v <= 1 {
			ratio = v
		} else {
			slog.Warn("obs: unparseable OTEL_TRACES_SAMPLER_ARG; falling back to ratio 1.0",
				slog.String("value", arg))
		}
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

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

	// Build the sampler once and pass it to BOTH branches so the export and
	// degraded paths never drift (L3 ruling; functionally moot in degraded mode
	// since nothing is exported, but keeps the T1.6 test honest).
	sampler := samplerFromEnv()

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler),
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
		sdktrace.WithSampler(sampler),
	), nil
}
