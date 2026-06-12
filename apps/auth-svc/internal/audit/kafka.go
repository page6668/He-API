// KafkaPublisher is the production audit.Publisher backed by Kafka topic
// `audit.event` (TS-CONS-015 — sharing the existing topic, retention 30d).
//
// TS-CONS-009 alignment ("async + acks=1 + non-blocking"): this Publisher
// is intentionally NOT responsible for the async semantics. The kafka.Writer
// wiring in cmd/server sets `Async: true` + `RequiredAcks: kafka.RequireOne`
// + a Completion callback that warn-logs publish failures. Under that
// configuration, `Publish` returns nil immediately after enqueueing — Kafka
// errors surface through the Completion callback, not through the return
// value. The handler call path (PublishBestEffort) is non-blocking by
// design even on broker outage (INT-065).
//
// For tests + sync writers, this Publisher faithfully propagates the
// underlying writer's error so UNIT-181 can verify that
// `PublishBestEffort` swallows + warn-logs — the contract the handler
// depends on.
//
// Partitioning (BR-4.5): the message key is `EmailHash` so all events for
// a given account land on the same partition in topic order. Empty key
// (events without a known email — e.g. invalid-token verify) round-robin
// across partitions, which is the kafka.Hash balancer's documented
// behavior.

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	obs "github.com/he-api/he-api/packages/go-observability"
)

// auditTracerName names the audit producer's tracer.
const auditTracerName = "apps/auth-svc/internal/audit"

// KafkaWriter is the narrow surface KafkaPublisher needs. `*kafka.Writer`
// satisfies it; the tests pass a fake recording writer so the audit unit
// tests don't require a real broker.
type KafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// KafkaPublisher implements Publisher over a Kafka writer.
type KafkaPublisher struct {
	writer KafkaWriter
	logger *slog.Logger
}

// NewKafkaPublisher constructs a publisher around the supplied writer.
// Logger defaults to slog.Default() when nil.
func NewKafkaPublisher(writer KafkaWriter, logger *slog.Logger) *KafkaPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &KafkaPublisher{writer: writer, logger: logger}
}

// Publish marshals the event as JSON (BR-4.5 schema) and writes one
// message to the underlying writer. The message Key is the event's
// EmailHash so the Hash balancer in cmd/server routes per-account
// events to the same partition.
//
// Return value: the underlying writer's error verbatim. Per TS-CONS-009
// the handler call sites wrap this in `PublishBestEffort` so business
// paths don't observe the error. In production the kafka.Writer is
// configured Async, so this returns nil immediately even when the
// broker is unreachable; the Completion callback logs the eventual
// error.
func (p *KafkaPublisher) Publish(ctx context.Context, event Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit marshal: %w", err)
	}
	msg := kafka.Message{
		Key:   []byte(event.EmailHash),
		Value: payload,
		Time:  event.Timestamp,
	}
	// Story 9.4 BR-TR-8/11 — inject W3C trace context into the headers when the
	// caller is inside a trace so the audit.event consumer links back to the
	// originating request; produced outside one, headers stay empty (BR-TR-11).
	var span trace.Span
	if trace.SpanContextFromContext(ctx).IsValid() {
		ctx, span = otel.Tracer(auditTracerName).Start(ctx, "audit.event produce",
			trace.WithSpanKind(trace.SpanKindProducer))
		obs.InjectKafkaHeaders(ctx, &msg)
	}
	err = p.writer.WriteMessages(ctx, msg)
	if span != nil {
		if err != nil {
			span.SetStatus(codes.Error, "produce failed") // static reason — no PII (BR-TR-7)
		}
		span.End()
	}
	return err
}
