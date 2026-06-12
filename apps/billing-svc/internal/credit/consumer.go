package credit

import (
	"context"
	"log/slog"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	obs "github.com/he-api/he-api/packages/go-observability"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// consumerTracerName names the credit consumer's tracer.
const consumerTracerName = "apps/billing-svc/internal/credit"

// Topic / consumer-group constants for the payment.completed stream.
const (
	Topic         = "payment.completed"
	ConsumerGroup = "billing-svc-credit"
)

// applier is the credit seam (satisfied by *Applier).
type applier interface {
	Apply(ctx context.Context, ev *paymentv1.PaymentEvent) (Outcome, error)
}

// MessageReader is the minimal segmentio reader surface (satisfied by
// *kafka.Reader; tests inject a fake). FetchMessage does NOT auto-commit.
type MessageReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Consumer runs the payment.completed consume loop: fetch → decode → Apply →
// commit the offset ONLY after a successful apply. A PG fault retains the offset
// (Kafka redelivers; the credit dedups via the pending→paid fence — BR-W-3/W-5).
// A malformed event is logged + committed (no head-of-line block; the provider's
// own webhook redelivery is the recovery path for a genuinely lost credit).
type Consumer struct {
	reader MessageReader
	apply  applier
	logger *slog.Logger
}

// NewConsumer builds a payment.completed Consumer.
func NewConsumer(reader MessageReader, apply applier, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{reader: reader, apply: apply, logger: logger}
}

// Run blocks consuming until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // graceful shutdown
			}
			c.logger.WarnContext(ctx, "credit_consume_fetch_failed",
				slog.String("event", "credit_consume_fetch_failed"),
				slog.String("error", err.Error()),
			)
			continue
		}
		// Story 9.4 BR-TR-9 — start a consumer span LINKED to the producing
		// request trace (link, not child — Q-KAFKA). Legacy header-less messages
		// root a fresh trace (back-compat).
		hctx, span := obs.StartConsumerSpan(ctx, consumerTracerName, Topic+" consume", msg)
		commit, herr := c.handle(hctx, msg)
		span.End()
		if herr != nil {
			// PG fault — retain the offset; Kafka redelivers, the fence dedups.
			c.logger.WarnContext(ctx, "credit_consume_retain_offset",
				slog.String("event", "credit_consume_retain_offset"),
				slog.String("error", herr.Error()),
			)
			continue
		}
		if commit {
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				c.logger.WarnContext(ctx, "credit_consume_commit_failed",
					slog.String("event", "credit_consume_commit_failed"),
					slog.String("error", err.Error()),
				)
			}
		}
	}
}

// handle processes one message. (true,nil) ⇒ commit; (false,err) ⇒ retain.
func (c *Consumer) handle(ctx context.Context, msg kafka.Message) (bool, error) {
	var ev paymentv1.PaymentEvent
	if err := protojson.Unmarshal(msg.Value, &ev); err != nil {
		// A malformed event cannot be reprocessed; commit it (the provider's webhook
		// redelivery is the recovery path). Never head-of-line-block the partition.
		c.logger.WarnContext(ctx, "credit_consume_malformed",
			slog.String("event", "credit_consume_malformed"),
			slog.String("error", err.Error()),
		)
		return true, nil
	}
	if _, err := c.apply.Apply(ctx, &ev); err != nil {
		return false, err // PG fault → retain offset
	}
	return true, nil // credited / duplicate / mismatch / ignored all ACK
}
