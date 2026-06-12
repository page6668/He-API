package obs

// Story 9.4: 全链路 trace（OpenTelemetry）— cross-service W3C trace-context
// propagation (the CONTINUITY layer atop Story 1.4's SDK + 3.6's he.request_id).
//
// Two root causes broke the chain before 9.4 (Architect Wright, verified vs HEAD):
//   1. no global TextMapPropagator was ever set → otelhttp's server handler used a
//      NoOp propagator and never extracted an incoming `traceparent`, so every
//      server span was a fresh ROOT;
//   2. no outbound client was instrumented (http_client.go fixes that).
//
// SetupPropagation installs root-cause #1's fix once, in the SHARED package, so
// all services share one wire format. The Kafka carrier extends the same global
// propagator to the async hops (segmentio/kafka-go has no auto-instrumentation).

import (
	"context"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// SetupPropagation installs the GLOBAL composite W3C propagator
// (`tracecontext` + `baggage`) used by every service (BR-TR-1, Q-PROP ratified).
//
//   - `tracecontext` carries trace_id/span_id — the continuity that makes a
//     downstream server span a CHILD of its caller (otelhttp extracts it).
//   - `baggage` is installed for wire-format completeness / third-party interop,
//     but OUR code injects NO baggage values: he.request_id is deterministically
//     derivable from trace_id (3.6: req_+hex(traceID[0:6])), so carrying it in a
//     cleartext baggage header is redundant AND needlessly widens the PII surface
//     (BR-TR-4 / Architect Q-PROP ruling).
//
// It is idempotent: calling it twice leaves exactly one valid composite
// propagator globally (otel.SetTextMapPropagator replaces, never accumulates).
// Call it in every cmd/server/main.go right after otel.SetTracerProvider — the
// same placement-before-use discipline as the meter provider. Propagation is
// independent of export: it is installed even in degraded mode (empty
// OTEL_EXPORTER_OTLP_ENDPOINT) so slog trace_id correlation keeps working.
func SetupPropagation() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
}

// kafkaHeaderCarrier adapts a *[]kafka.Header to propagation.TextMapCarrier so
// the global propagator can inject/extract W3C fields over Kafka message headers
// (BR-TR-8/9). It is additive: Set overwrites a same-key header in place and only
// appends new keys, so the pre-existing manual RequestId header (3.6) and any
// other headers are preserved.
type kafkaHeaderCarrier struct {
	headers *[]kafka.Header
}

// Get returns the first header value for key, or "" (TextMapCarrier contract).
func (c kafkaHeaderCarrier) Get(key string) string {
	for _, h := range *c.headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Set writes key=value, replacing an existing same-key header in place (idempotent
// re-inject) or appending a new one — never duplicating, never dropping others.
func (c kafkaHeaderCarrier) Set(key, value string) {
	for i := range *c.headers {
		if (*c.headers)[i].Key == key {
			(*c.headers)[i].Value = []byte(value)
			return
		}
	}
	*c.headers = append(*c.headers, kafka.Header{Key: key, Value: []byte(value)})
}

// Keys lists every header key present (TextMapCarrier contract).
func (c kafkaHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(*c.headers))
	for _, h := range *c.headers {
		keys = append(keys, h.Key)
	}
	return keys
}

// InjectKafkaHeaders writes the current span context (from ctx) into msg.Headers
// as W3C `traceparent`/`tracestate` via the global propagator (BR-TR-8). It is
// additive — existing headers (incl. the 3.6 RequestId) are preserved. Callers
// MUST only call this when ctx carries an active span (see the producer wiring):
// outside a trace the propagator writes nothing, so a header-less message is
// produced and the consumer correctly roots a new trace (BR-TR-11).
//
// PII rule (BR-TR-12): because our code injects NO baggage values and the
// propagator only emits trace-context fields, the headers carry ONLY
// traceparent/tracestate (+ the pre-existing RequestId) — never user_id, email,
// api-key, or model input.
func InjectKafkaHeaders(ctx context.Context, msg *kafka.Message) {
	otel.GetTextMapPropagator().Inject(ctx, kafkaHeaderCarrier{headers: &msg.Headers})
}

// ExtractKafkaHeaders reconstitutes the upstream span context from msg.Headers
// and returns a context carrying it (BR-TR-9). The consumer then starts its
// processing span with a span LINK to this context (Q-KAFKA ruling: link, not
// child, for ALL 4 topics — they are at-least-once + fan-out, so a strict
// child-parent would distort the producer's latency tree and mis-parent a
// redelivered message). A header-less or malformed message yields a context with
// no valid remote span context, so the consumer roots a new trace without error
// (BR-TR-9 back-compat, ERROR-002).
func ExtractKafkaHeaders(ctx context.Context, msg kafka.Message) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, kafkaHeaderCarrier{headers: &msg.Headers})
}

// StartConsumerSpan extracts the upstream trace context from msg and starts a
// NEW-ROOT consumer span LINKED to the producer (Q-KAFKA ratified ruling: a span
// LINK, not a parent/child, for ALL 4 topics — they are at-least-once + fan-out,
// so a strict child would distort the producer's latency tree and mis-parent a
// redelivered message). It encodes that ruling once for every consumer.
//
// The span's parent is whatever ctx carries (the consume loop has no active span
// → the span is a NEW root). A header-less or malformed message yields no valid
// link, so the consumer simply roots a fresh trace with no error (back-compat,
// ERROR-002). The caller MUST End() the returned span.
func StartConsumerSpan(ctx context.Context, tracerName, spanName string, msg kafka.Message) (context.Context, trace.Span) {
	linkCtx := ExtractKafkaHeaders(ctx, msg)
	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindConsumer)}
	if l := trace.LinkFromContext(linkCtx); l.SpanContext.IsValid() {
		opts = append(opts, trace.WithLinks(l))
	}
	return otel.Tracer(tracerName).Start(ctx, spanName, opts...)
}
