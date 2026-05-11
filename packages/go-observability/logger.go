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
// level and stamps every record with trace_id / span_id when the caller's
// context carries an active span.
func NewLogger(level slog.Level) *slog.Logger {
	json := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(NewTraceContextHandler(json))
}
