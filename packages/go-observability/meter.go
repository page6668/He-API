// Package obs — OTel meter provider with Prometheus exporter.
//
// Story 2.2 P5d (T4.7 + BR-4.8) — parallel to NewTracerProvider. The
// provider's reader is a Prometheus exporter that registers metric
// instruments on `prometheus.DefaultRegisterer`. WrapHTTPHandler already
// exposes `/metrics` via `promhttp.Handler()` (which by default serves
// the DefaultRegisterer), so once a service installs this meter provider
// as the global OTel meter provider, its OTel counters appear in the
// Prometheus scrape output automatically — no extra HTTP handler wiring.

package obs

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// NewMeterProvider returns an OTel SDK MeterProvider that exports through
// a Prometheus exporter registered on prometheus.DefaultRegisterer. The
// resource carries service.{name,namespace,version} stamps (matches the
// TracerProvider so Prometheus + Tempo share the same service identity).
//
// Pass the returned provider to otel.SetMeterProvider BEFORE any package
// calls Meter() — instruments registered against the no-op provider are
// orphaned and won't switch over.
//
// Shutdown: caller MUST defer provider.Shutdown(ctx) so the Prometheus
// exporter's registration is cleaned up — important for tests that
// install + tear down providers across cases.
func NewMeterProvider(ctx context.Context, name, namespace, version string) (*sdkmetric.MeterProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(name),
			semconv.ServiceNamespace(namespace),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("obs: meter provider resource: %w", err)
	}

	// prometheus.New is the OTel→Prometheus bridge exporter; by default
	// it registers instruments on prometheus.DefaultRegisterer.
	exp, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("obs: prometheus exporter: %w", err)
	}

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(exp),
	), nil
}
