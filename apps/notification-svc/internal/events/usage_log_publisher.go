// usage_log_publisher.go — Story 9.3 BR-EX-9. Publishes the
// usage.log.export.requested Kafka event (NEW topic; partitions=6,
// retention=7d). Mirrors GDPRExportPublisher; partition key = user_id
// (TS-CONS-015). The event carries format + the resolved [range_start,
// range_end] window the analytics-svc worker needs to dump request_logs.
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	obs "github.com/he-api/he-api/packages/go-observability"
	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"
)

// usageLogTracerName names the usage-log-export producer's tracer.
const usageLogTracerName = "apps/notification-svc/internal/events"

// TopicUsageLogExportRequested is the Kafka topic name (matches
// `usage.log.export.requested` declared in the kafka-topics Terraform module).
const TopicUsageLogExportRequested = "usage.log.export.requested"

// UsageLogExportPublisher produces UsageLogExportRequestedEvent proto messages.
type UsageLogExportPublisher struct {
	writer KafkaWriter
	logger *slog.Logger
}

// NewUsageLogExportPublisher constructs a publisher around the supplied writer.
func NewUsageLogExportPublisher(writer KafkaWriter, logger *slog.Logger) *UsageLogExportPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &UsageLogExportPublisher{writer: writer, logger: logger}
}

// Publish marshals the proto event and writes one message to the topic with
// key=user_id. Called AFTER the PG INSERT commits (BR-EX-9 / BR-EX-1 outbox
// discipline) so a Kafka outage leaves the row at `pending` (redrive-safe).
func (p *UsageLogExportPublisher) Publish(ctx context.Context, exportID, userID, format string, rangeStart, rangeEnd time.Time) error {
	if p == nil || p.writer == nil {
		return errors.New("events: usage-log publisher not configured")
	}
	if exportID == "" || userID == "" {
		return errors.New("events: export_id and user_id required")
	}
	payload := &usagelogv1.UsageLogExportRequestedEvent{
		ExportId:   exportID,
		UserId:     userID,
		Format:     format,
		RangeStart: timestamppb.New(rangeStart),
		RangeEnd:   timestamppb.New(rangeEnd),
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		return fmt.Errorf("events: marshal usage-log event: %w", err)
	}
	msg := kafka.Message{
		Topic: TopicUsageLogExportRequested,
		Key:   []byte(userID),
		Value: body,
		Time:  rangeEnd,
	}
	// Story 9.4 BR-TR-8/11 — inject W3C trace context when inside a trace so the
	// analytics-svc export worker links back to the request that asked for the dump.
	var span trace.Span
	if trace.SpanContextFromContext(ctx).IsValid() {
		ctx, span = otel.Tracer(usageLogTracerName).Start(ctx, TopicUsageLogExportRequested+" produce",
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
