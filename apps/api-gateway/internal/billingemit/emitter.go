// Package billingemit is the gateway-side usage.recorded PRODUCER (Story 7.1
// AC2 T2.5 — Architect Q-PRODUCER: the api-gateway is the producer; billing-svc
// is the consumer). After each SUCCESSFUL completion the chat handler emits one
// UsageEvent per billable leg, fire-and-forget: a producer error NEVER fails the
// user's response (the response is already flushed), it only logs +
// increments he_billing_emit_failures_total (BR-D-4).
//
// The event carries RAW inputs only — NO cost_usd field (Architect Q-CH:
// billing-svc is the sole cost authority). On the wire it is protojson
// (consumer uses the same codec). The Kafka producer is configured acks=all
// (Architect Q-KCLIENT override of BR-D-4 — a lost charge event is unacceptable
// on the money path).
package billingemit

import (
	"context"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"

	obs "github.com/he-api/he-api/packages/go-observability"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// tracerName names the producer's tracer (parity with the meter name above).
const tracerName = "apps/api-gateway/internal/billingemit"

// writeTimeout bounds the detached produce so a stuck broker cannot leak
// goroutines indefinitely.
const writeTimeout = 10 * time.Second

// Topic is the usage.recorded Kafka topic (data-models §4.4).
const Topic = "usage.recorded"

// UsageEmitter emits a usage event. Implementations MUST be fire-and-forget —
// Emit never blocks the caller meaningfully and never surfaces an error (the
// chat response already succeeded).
type UsageEmitter interface {
	Emit(ctx context.Context, ev *billingv1.UsageEvent)
}

// Nop is the default no-op emitter (billing disabled / tests).
type Nop struct{}

// Emit does nothing.
func (Nop) Emit(context.Context, *billingv1.UsageEvent) {}

// MessageWriter is the minimal segmentio writer surface (satisfied by
// *kafka.Writer; tests inject a fake). With an Async writer WriteMessages
// returns immediately (the broker ack + any error surface via the writer's
// Completion callback).
type MessageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// KafkaEmitter produces usage.recorded events.
type KafkaEmitter struct {
	w        MessageWriter
	logger   *slog.Logger
	failures metric.Int64Counter
	onDone   func() // test hook: invoked when a detached produce attempt completes
}

// NewKafkaEmitter builds a producer over the supplied writer. logger may be nil.
func NewKafkaEmitter(w MessageWriter, logger *slog.Logger) *KafkaEmitter {
	if logger == nil {
		logger = slog.Default()
	}
	failures, _ := otel.Meter("apps/api-gateway/internal/billingemit").Int64Counter(
		"he_billing_emit_failures_total",
		metric.WithDescription("usage.recorded producer failures (response unaffected — fire-and-forget)"),
	)
	return &KafkaEmitter{w: w, logger: logger, failures: failures}
}

// Emit marshals the event (protojson) and writes it keyed by user_id (partition
// stability). The produce runs in a DETACHED goroutine (the caller's request ctx
// is cancelled the moment the handler returns) with a bounded timeout, so it
// never delays the connection's reuse and never affects the already-flushed
// response (BR-D-4). A marshal/write error is swallowed — logged + counted.
func (e *KafkaEmitter) Emit(ctx context.Context, ev *billingv1.UsageEvent) {
	body, err := protojson.Marshal(ev)
	if err != nil {
		e.fail(ctx, "marshal", ev, err)
		return
	}
	msg := kafka.Message{
		Topic: Topic,
		Key:   []byte(ev.GetUserId()),
		Value: body,
	}
	// Story 9.4 BR-TR-8/11 — inject W3C trace context into the kafka headers so
	// billing-svc's credit-apply span links back to the originating request trace.
	// Only when the caller is inside a trace; produced outside one (cold path) the
	// headers stay empty and the consumer correctly roots a new trace (BR-TR-11).
	// The producer span is opened on the request ctx (active trace) BEFORE detach.
	var span trace.Span
	pctx := ctx
	if trace.SpanContextFromContext(ctx).IsValid() {
		pctx, span = otel.Tracer(tracerName).Start(ctx, Topic+" produce",
			trace.WithSpanKind(trace.SpanKindProducer))
		obs.InjectKafkaHeaders(pctx, &msg)
	}
	go func() {
		// Detach from the request ctx (which is cancelled on handler return) but
		// keep a bounded deadline. acks=all is configured on the writer.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(pctx), writeTimeout)
		defer cancel()
		if span != nil {
			defer span.End()
		}
		if err := e.w.WriteMessages(wctx, msg); err != nil {
			if span != nil {
				span.SetStatus(codes.Error, "produce failed") // static reason — no PII (BR-TR-7)
			}
			e.fail(wctx, "write", ev, err)
		}
		e.done()
	}()
}

// done signals one completed produce attempt (tests hook this; nil in prod).
func (e *KafkaEmitter) done() {
	if e.onDone != nil {
		e.onDone()
	}
}

func (e *KafkaEmitter) fail(ctx context.Context, stage string, ev *billingv1.UsageEvent, err error) {
	if e.failures != nil {
		e.failures.Add(ctx, 1)
	}
	e.logger.WarnContext(ctx, "billing_emit_failed",
		slog.String("event", "billing_emit_failed"),
		slog.String("stage", stage),
		slog.String("he_request_id", ev.GetHeRequestId()),
		slog.String("error", err.Error()),
	)
}
