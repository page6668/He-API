// KafkaPublisher implements audit.Publisher over the existing `audit.event`
// Kafka topic (Story 2.2 TS-CONS-015 reuse — no new topic creation).
//
// Mirrors apps/auth-svc/internal/audit/kafka.go contract:
//   - the wrapped *kafka.Writer is configured async + acks=1 in main.go
//   - this Publisher's Publish returns the writer's error so unit tests
//     can verify PublishBestEffort's swallow-and-warn behavior
//   - partition key = user_id (TS-CONS-015 partition stability)
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/segmentio/kafka-go"
)

// KafkaWriter is the narrow surface KafkaPublisher needs.
type KafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// KafkaPublisher implements Publisher over a Kafka writer.
type KafkaPublisher struct {
	writer KafkaWriter
	logger *slog.Logger
}

// NewKafkaPublisher constructs a publisher around the supplied writer.
func NewKafkaPublisher(writer KafkaWriter, logger *slog.Logger) *KafkaPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &KafkaPublisher{writer: writer, logger: logger}
}

// Publish marshals the event as JSON and writes one message to the topic
// with key=user_id. The Hash balancer in main.go routes per-user events
// to the same partition (BR-6.1).
func (p *KafkaPublisher) Publish(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	msg := kafka.Message{
		Topic: "audit.event",
		Key:   []byte(event.UserID),
		Value: body,
		Time:  event.Timestamp,
	}
	return p.writer.WriteMessages(ctx, msg)
}
