package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TestHelloRoute checks `/hello?name=test` returns 200 with a JSON body that
// includes the supplied name. Boundary case `name=""` is exercised in
// TestHelloEmptyName below (Round 1 1.4-BLIND-BOUNDARY-001).
func TestHelloRoute(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(buildHandler(logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/hello?name=test")
	if err != nil {
		t.Fatalf("GET /hello failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "test") {
		t.Fatalf("expected body to contain name 'test', got %s", string(body))
	}
}

// TestHelloEmptyName — Round 1 1.4-BLIND-BOUNDARY-001 — empty `name` is a
// success path with default greeting (`world`), not a 5xx and not a panic.
func TestHelloEmptyName(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(buildHandler(logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/hello?name=")
	if err != nil {
		t.Fatalf("GET /hello?name= failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body failed: %v", err)
	}
	if !strings.Contains(body["greeting"], "world") {
		t.Fatalf("expected default greeting to mention 'world', got %q", body["greeting"])
	}
}

// TestMetricsEndpoint asserts `/metrics` returns 200 and the body contains at
// least one Prometheus counter family. We expect the Go/promhttp default
// metrics to be present (e.g., `promhttp_metric_handler_requests_total`); when
// otelhttp instrumentation runs, the OTel exporter adds
// `http_server_request_duration_seconds_count` — we check either is present.
func TestMetricsEndpoint(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(buildHandler(logger))
	defer srv.Close()

	// One request so the otelhttp histogram has at least one observation.
	_, _ = http.Get(srv.URL + "/hello?name=warm")

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected /metrics 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	if !strings.Contains(out, "http_server_request_duration_seconds_count") &&
		!strings.Contains(out, "promhttp_metric_handler_requests_total") &&
		!strings.Contains(out, "go_goroutines") {
		t.Fatalf("expected /metrics body to contain a known counter; got prefix %.200q", out)
	}
}

// TestTraceIDLogField asserts the slog handler emits `trace_id` when the
// caller passes a context bound to a real span. This is the structural
// guarantee that makes the Loki → Jaeger derived-field linkage in
// `infra/helm/observability/kube-prometheus-stack/templates/grafana-datasources.yaml`
// actually work at runtime.
func TestTraceIDLogField(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	tr := tp.Tracer("sample-otel-app/test")
	ctx, span := tr.Start(context.Background(), "TestTraceIDLogField")
	defer span.End()

	buf := &bytes.Buffer{}
	inner := slog.NewJSONHandler(buf, nil)
	logger := slog.New(&traceContextHandler{inner: inner})

	logger.InfoContext(ctx, "hello from a span")

	if !strings.Contains(buf.String(), `"trace_id"`) {
		t.Fatalf("expected log line to contain trace_id field, got %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"span_id"`) {
		t.Fatalf("expected log line to contain span_id field, got %s", buf.String())
	}
}
