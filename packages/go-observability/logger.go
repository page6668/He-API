package obs

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// TraceContextHandler is a slog.Handler that adds `trace_id` and `span_id`
// attributes derived from the context-bound span, when one is present. This
// is the seam that links structured log lines back to a Jaeger trace.
type TraceContextHandler struct {
	inner slog.Handler
}

// NewTraceContextHandler wraps any slog.Handler with span-context enrichment.
// Exported so callers who already constructed their own JSON handler can opt
// in without re-creating the chain.
func NewTraceContextHandler(inner slog.Handler) *TraceContextHandler {
	return &TraceContextHandler{inner: inner}
}

func (h *TraceContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *TraceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if sc := span.SpanContext(); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithGroup(name)}
}

// NewLogger builds a slog.Logger that writes JSON to stdout at the requested
// level, redacts sensitive attribute values (Story 2.4 m-2 / BR-5.8), and
// stamps every record with trace_id / span_id when the caller's context
// carries an active span.
//
// Handler chain (outermost first): TraceContextHandler → RedactionHandler →
// JSON. Redaction sits BEFORE the JSON encoder so the [REDACTED] substitution
// is visible in the serialized output; trace enrichment is outermost so
// trace_id / span_id are stamped on every record regardless of redaction.
func NewLogger(level slog.Level) *slog.Logger {
	json := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	redacted := NewRedactionHandler(json, nil)
	return slog.New(NewTraceContextHandler(redacted))
}
