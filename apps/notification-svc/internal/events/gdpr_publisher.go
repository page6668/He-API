// Package events publishes Kafka domain events produced by notification-svc.
//
// Story 2.6 introduces the gdpr.export.requested topic (NEW per AC2 BR-2.6
// + T0.3 Terraform module). Partition key = user_id (TS-CONS-015 partition
// stability — same key the audit.event topic uses for ordering parity).
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

	gdprv1 "github.com/he-api/he-api/packages/proto/gen/go/he/gdpr/v1"
)

// TopicGDPRExportRequested is the Kafka topic name (matches the
// `gdpr.export.requested` declared in infra/terraform/modules/kafka-topics
// per T0.3).
const TopicGDPRExportRequested = "gdpr.export.requested"

// KafkaWriter is the narrow surface this package needs. `*kafka.Writer`
// satisfies it; tests pass a fake recorder.
type KafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// GDPRExportPublisher produces DataExportRequestedEvent proto messages.
type GDPRExportPublisher struct {
	writer KafkaWriter
	logger *slog.Logger
}

// NewGDPRExportPublisher constructs a publisher around the supplied writer.
func NewGDPRExportPublisher(writer KafkaWriter, logger *slog.Logger) *GDPRExportPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &GDPRExportPublisher{writer: writer, logger: logger}
}

// Publish marshals the proto event and writes one message to the topic
// with key=user_id. Returns the underlying writer's error so the caller
// can decide whether to roll back the surrounding PG transaction.
//
// AC2 BR-2.5 contract: the caller invokes Publish AFTER the PG INSERT
// commits, so that a Kafka outage leaves the row in `pending` (audited
// by the future BR-4.7 cron) rather than producing a Kafka event for a
// non-existent row.
func (p *GDPRExportPublisher) Publish(ctx context.Context, exportID, userID string, requestedAt time.Time) error {
	if p == nil || p.writer == nil {
		return errors.New("events: gdpr publisher not configured")
	}
	if exportID == "" || userID == "" {
		return errors.New("events: export_id and user_id required")
	}
	payload := &gdprv1.DataExportRequestedEvent{
		ExportId:    exportID,
		UserId:      userID,
		RequestedAt: timestamppb.New(requestedAt),
	}
	body, err := proto.Marshal(payload)
	if err != nil {
		return fmt.Errorf("events: marshal: %w", err)
	}
	msg := kafka.Message{
		Topic: TopicGDPRExportRequested,
		Key:   []byte(userID),
		Value: body,
		Time:  requestedAt,
	}
	return p.writer.WriteMessages(ctx, msg)
}
