package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace"
)

// 1.5-UNIT-T5-003: TraceContextHandler injects trace_id + span_id when the
// context carries a valid span (AC1.D — UNIT-204 contract).
func TestTraceContextHandler_InjectsTraceAndSpanID(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewTraceContextHandler(inner))

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	logger.InfoContext(ctx, "hello")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v — raw=%q", err, buf.String())
	}
	traceID, ok := rec["trace_id"].(string)
	if !ok || traceID == "" {
		t.Fatalf("expected non-empty trace_id field; got %v", rec)
	}
	spanID, ok := rec["span_id"].(string)
	if !ok || spanID == "" {
		t.Fatalf("expected non-empty span_id field; got %v", rec)
	}
	if !strings.Contains(buf.String(), `"hello"`) {
		t.Errorf("expected log message to be present; got %q", buf.String())
	}
}

// 1.5-UNIT-T5-004: when no span is in context, no trace_id key is emitted
// (no spurious empty strings — the field is simply absent).
func TestTraceContextHandler_NoSpanNoFields(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(NewTraceContextHandler(inner))

	logger.InfoContext(context.Background(), "no-span")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v", err)
	}
	if _, present := rec["trace_id"]; present {
		t.Errorf("trace_id must not be present without an active span; got %v", rec)
	}
	if _, present := rec["span_id"]; present {
		t.Errorf("span_id must not be present without an active span; got %v", rec)
	}
}

// NewLogger smoke test: must return a non-nil logger that writes JSON to stdout.
func TestNewLogger_NotNil(t *testing.T) {
	if NewLogger(slog.LevelInfo) == nil {
		t.Fatal("NewLogger returned nil")
	}
}
