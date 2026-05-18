package obs

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// RequestIDExtractor returns the per-request he_request_id stamped on ctx by
// the gateway's requestid middleware. The function signature matches
// apps/api-gateway/internal/middleware/requestid.FromContext exactly.
//
// Defined here (in packages/go-observability) to avoid a hard dependency from
// packages/ to apps/ — the gateway wires the concrete FromContext at startup
// via NewLogger(level, WithRequestIDExtractor(requestid.FromContext)).
type RequestIDExtractor func(ctx context.Context) (string, bool)

// LoggerOption configures NewLogger. Story 3.6 (Architect Round 1 OQ5
// RATIFIED) added WithRequestIDExtractor so he_request_id flows through the
// slog handler chain without per-call-site discipline (BR-2.10).
type LoggerOption func(*loggerConfig)

type loggerConfig struct {
	requestIDExtractor RequestIDExtractor
}

// WithRequestIDExtractor wires a function that reads the per-request
// he_request_id from a context. The TraceContextHandler will then stamp
// `he_request_id` on every slog record when the extractor returns ok=true.
func WithRequestIDExtractor(extractor RequestIDExtractor) LoggerOption {
	return func(c *loggerConfig) {
		c.requestIDExtractor = extractor
	}
}

// TraceContextHandler is a slog.Handler that adds `trace_id`, `span_id`, and
// (when an extractor is wired) `he_request_id` attributes derived from the
// context-bound span and request-id middleware. This is the seam that links
// structured log lines back to a Jaeger trace + customer-support correlation.
type TraceContextHandler struct {
	inner              slog.Handler
	requestIDExtractor RequestIDExtractor
}

// NewTraceContextHandler wraps any slog.Handler with span-context enrichment.
// Exported so callers who already constructed their own JSON handler can opt
// in without re-creating the chain.
func NewTraceContextHandler(inner slog.Handler) *TraceContextHandler {
	return &TraceContextHandler{inner: inner}
}

// NewTraceContextHandlerWithRequestID wraps any slog.Handler with both
// span-context AND he_request_id enrichment. Story 3.6 BR-2.10.
func NewTraceContextHandlerWithRequestID(inner slog.Handler, extractor RequestIDExtractor) *TraceContextHandler {
	return &TraceContextHandler{inner: inner, requestIDExtractor: extractor}
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
	if h.requestIDExtractor != nil {
		if id, ok := h.requestIDExtractor(ctx); ok && id != "" {
			r.AddAttrs(slog.String("he_request_id", id))
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithAttrs(attrs), requestIDExtractor: h.requestIDExtractor}
}

func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithGroup(name), requestIDExtractor: h.requestIDExtractor}
}

// NewLogger builds a slog.Logger that writes JSON to stdout at the requested
// level, redacts sensitive attribute values (Story 2.4 m-2 / BR-5.8), and
// stamps every record with trace_id / span_id (and he_request_id, when a
// RequestIDExtractor is wired via WithRequestIDExtractor) for any record
// whose context carries an active span / stamped request-id.
//
// Handler chain (outermost first): TraceContextHandler → RedactionHandler →
// JSON. Redaction sits BEFORE the JSON encoder so the [REDACTED] substitution
// is visible in the serialized output; trace enrichment is outermost so
// trace_id / span_id / he_request_id are stamped on every record regardless
// of redaction.
func NewLogger(level slog.Level, opts ...LoggerOption) *slog.Logger {
	var cfg loggerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	json := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	redacted := NewRedactionHandler(json, nil)
	return slog.New(NewTraceContextHandlerWithRequestID(redacted, cfg.requestIDExtractor))
}
