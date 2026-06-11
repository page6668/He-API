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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"
)

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
	return p.writer.WriteMessages(ctx, msg)
}
