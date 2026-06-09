// Package consumer is billing-svc's usage.recorded Kafka consumer (Story 7.1
// AC2 T2.3/T2.4). It is the at-least-once side of the exactly-once invariant:
// fetch → decode → ledger.Apply → commit the offset ONLY after a successful
// apply (BR-D-2). A PG fault retains the offset (Kafka redelivers; the ledger
// dedups via ledger_key). A no-pricing or malformed event is parked to the DLQ
// topic (Q-DLQ — never infinite-retry head-of-line-block) and the offset is
// committed.
package consumer

import (
	"context"
	"log/slog"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/he-api/he-api/apps/billing-svc/internal/ledger"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// Topic / consumer-group constants (data-models §4.4 + Q-DLQ).
const (
	Topic         = "usage.recorded"
	DLQTopic      = "usage.recorded.dlq"
	ConsumerGroup = "billing-svc"
)

// Applier is the ledger seam (satisfied by *ledger.Ledger).
type Applier interface {
	Apply(ctx context.Context, ev *billingv1.UsageEvent) (ledger.Result, error)
}

// MessageReader is the minimal segmentio reader surface (satisfied by
// *kafka.Reader; tests inject a fake). FetchMessage does NOT auto-commit —
// CommitMessages is called explicitly after a successful apply.
type MessageReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
}

// DLQProducer parks un-processable events (satisfied by *kafka.Writer).
type DLQProducer interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Consumer runs the usage.recorded consume loop.
type Consumer struct {
	reader MessageReader
	dlq    DLQProducer
	apply  Applier
	logger *slog.Logger
}

// New builds a Consumer. logger may be nil (slog.Default()). dlq may be nil
// (a no-pricing/malformed event then retains its offset rather than parking —
// degraded, but never silently dropped).
func New(reader MessageReader, dlq DLQProducer, apply Applier, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{reader: reader, dlq: dlq, apply: apply, logger: logger}
}

// Run blocks consuming until ctx is cancelled. A fetch error on a cancelled ctx
// is a clean shutdown; any other fetch error is logged and retried.
func (c *Consumer) Run(ctx context.Context) {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // graceful shutdown (BLIND-RESOURCE-003)
			}
			c.logger.WarnContext(ctx, "billing_consume_fetch_failed",
				slog.String("event", "billing_consume_fetch_failed"),
				slog.String("error", err.Error()),
			)
			continue
		}

		commit, herr := c.handle(ctx, msg)
		if herr != nil {
			// Retain the offset — the message will be redelivered (PG fault, or a
			// DLQ-park that itself failed). The ledger dedups a redelivery.
			c.logger.WarnContext(ctx, "billing_consume_retain_offset",
				slog.String("event", "billing_consume_retain_offset"),
				slog.String("error", herr.Error()),
			)
			continue
		}
		if commit {
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				c.logger.WarnContext(ctx, "billing_consume_commit_failed",
					slog.String("event", "billing_consume_commit_failed"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// handle processes one message. Returns (commit, err):
//   - (true, nil)  — applied/duplicate/parked-to-DLQ → commit the offset.
//   - (false, err) — PG fault, or a DLQ park that itself failed → retain the
//     offset (Kafka redelivers; the ledger dedups). NEVER drop a billable event.
//
// Exported indirectly via Run; kept package-visible for direct unit testing
// (no real Kafka).
func (c *Consumer) handle(ctx context.Context, msg kafka.Message) (bool, error) {
	var ev billingv1.UsageEvent
	if err := protojson.Unmarshal(msg.Value, &ev); err != nil {
		// BLIND-ERROR-002 — a malformed/garbage event cannot be reprocessed;
		// park it to the DLQ and commit (no panic, no infinite redelivery).
		c.logger.WarnContext(ctx, "billing_consume_malformed",
			slog.String("event", "billing_consume_malformed"),
			slog.String("error", err.Error()),
		)
		return c.park(ctx, msg, "malformed")
	}

	res, err := c.apply.Apply(ctx, &ev)
	if err != nil {
		// PG fault — retain the offset; Kafka redelivers when PG recovers
		// (at-least-once + idempotent, BR-D-2).
		return false, err
	}

	switch res.Outcome {
	case ledger.OutcomeNoPricing:
		// BR-D-8 / Q-DLQ — park to the DLQ + alert; user not charged, event not
		// lost. A backoff-retry would head-of-line-block the partition.
		return c.park(ctx, msg, "no_pricing")
	default:
		// Applied or Duplicate — both ACK (exactly-once already enforced).
		return true, nil
	}
}

// park writes the message to the DLQ topic and reports whether the offset may be
// committed. A DLQ-write failure retains the offset (BLIND-ERROR-003 — no silent
// loss).
func (c *Consumer) park(ctx context.Context, msg kafka.Message, reason string) (bool, error) {
	if c.dlq == nil {
		// No DLQ wired — retain the offset rather than drop the event.
		return false, errNoDLQ
	}
	dlqMsg := kafka.Message{
		Topic:   DLQTopic,
		Key:     msg.Key,
		Value:   msg.Value,
		Headers: append(msg.Headers, kafka.Header{Key: "x-dlq-reason", Value: []byte(reason)}),
	}
	if err := c.dlq.WriteMessages(ctx, dlqMsg); err != nil {
		c.logger.WarnContext(ctx, "billing_dlq_write_failed",
			slog.String("event", "billing_dlq_write_failed"),
			slog.String("reason", reason),
			slog.String("error", err.Error()),
		)
		return false, err // retain offset — no silent loss (BLIND-ERROR-003)
	}
	c.logger.WarnContext(ctx, "billing_event_dead_lettered",
		slog.String("event", "billing_event_dead_lettered"),
		slog.String("reason", reason),
	)
	return true, nil
}

// errNoDLQ marks a missing DLQ producer (offset retained, event preserved).
var errNoDLQ = errNoDLQError{}

type errNoDLQError struct{}

func (errNoDLQError) Error() string { return "consumer: no DLQ producer wired; offset retained" }
