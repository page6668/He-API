// sample-otel-app — Epic 1 reference implementation (Story 1.4).
//
// Smallest possible HTTP service that exercises the full Story 1.4 observability
// pyramid:
//   * OTLP/gRPC trace export to the otel-collector deployment in the
//     `monitoring` namespace (env OTEL_EXPORTER_OTLP_ENDPOINT).
//   * Prometheus `/metrics` endpoint scraped by the kube-prometheus-stack
//     Prometheus via the ServiceMonitor in infra/helm/sample-otel-app.
//   * `log/slog` JSON handler that stamps every line with `trace_id` and
//     `span_id` extracted via `trace.SpanFromContext` (Round 1 M-1).
//
// Resource attrs declared:
//   service.name      = sample-otel-app
//   service.namespace = he-api-staging
//   service.version   = 0.1.0
//
// `k8s.*` attrs are deliberately NOT hardcoded here — the otel-collector
// `k8sattributes` processor backfills them from pod metadata (Round 1 contract
// 1.4-UNIT-208; see infra/helm/observability/otel-collector/values-staging.yaml).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName    = "sample-otel-app"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.1.0"
	listenAddr     = ":8080"
)

// newTracerProvider configures an OTLP/gRPC TracerProvider rooted at the
// otel-collector specified by OTEL_EXPORTER_OTLP_ENDPOINT. When the env var is
// empty, returns a no-export provider so the service still serves traffic
// (degraded mode — Round 1 1.4-UNIT-210 contract).
func newTracerProvider(ctx context.Context, logger *slog.Logger) (*sdktrace.TracerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceNamespace(serviceNS),
			semconv.ServiceVersion(serviceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// Degraded mode: no exporter, but the SDK still produces span contexts
		// that the slog handler can stamp on log lines. Operators reading
		// logs still get trace_id-correlated lines; the trace just does not
		// reach the collector.
		logger.Warn("OTEL_EXPORTER_OTLP_ENDPOINT not set; starting in no-export degraded mode")
		return sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
		), nil
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otlptracegrpc.New: %w", err)
	}

	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithBatchTimeout(5*time.Second),
		),
		sdktrace.WithResource(res),
	), nil
}

// traceContextHandler returns a slog.Handler that adds `trace_id` and `span_id`
// attributes from the context-bound span, when one is present. This is what
// links a JSON log line back to a Jaeger trace in the Story 1.4 stack.
type traceContextHandler struct {
	inner slog.Handler
}

func (h *traceContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if sc := span.SpanContext(); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h *traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceContextHandler) WithGroup(name string) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithGroup(name)}
}

// helloHandler answers `GET /hello?name=…`. Always responds 200 with a JSON
// body so the OTel autoinstrumented histogram bucket is well-formed and the
// `1.4-BLIND-BOUNDARY-001` (empty name) case stays a success path.
func helloHandler(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			name = "world"
		}
		logger.InfoContext(r.Context(), "served hello", slog.String("name", name))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"greeting": "hello, " + name,
			"service":  serviceName,
		})
	}
}

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func buildHandler(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", healthzHandler)
	mux.Handle("/hello", http.HandlerFunc(helloHandler(logger)))

	// otelhttp wraps the mux so every request creates a server span
	// (semconv `http_server_request_duration_seconds_*` histogram is emitted
	// by the OTel SDK + the prometheus exporter inside the otel-collector).
	return otelhttp.NewHandler(mux, "sample-otel-app")
}

func main() {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(&traceContextHandler{inner: jsonHandler})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tp, err := newTracerProvider(ctx, logger)
	if err != nil {
		logger.Error("tracer provider init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	otel.SetTracerProvider(tp)
	defer func() {
		// Graceful flush on SIGTERM — Round 1 1.4-BLIND-RESOURCE-001 / -209
		// contract: in-flight spans must reach the collector before the
		// process exits.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			logger.Error("tracer provider shutdown failed", slog.String("error", err.Error()))
		}
	}()

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           buildHandler(logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("sample-otel-app listening", slog.String("addr", listenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serverErr:
		logger.Error("http server error", slog.String("error", err.Error()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", slog.String("error", err.Error()))
	}
}
