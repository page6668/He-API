// Package producer publishes the `payment.completed` Kafka event (Story 7.3,
// data-models §4.4). payment-svc is the PRODUCER (on a signature-verified
// provider webhook); billing-svc (credit / subscription renew) + notification-svc
// (receipt email) are CONSUMERS. The payload is a he.payment.v1.PaymentEvent
// JSON-serialized with protojson — mirroring the usage.recorded precedent so the
// billing-svc consumer decodes it the same way (7.1 Q-KCLIENT cascade).
//
// acks=all (RequireAll): the money-IN signal must be durable before the webhook
// 2xx ACKs the provider; a produce failure surfaces so the webhook returns 5xx
// and the provider redelivers (at-least-once + idempotent downstream, BR-W-5).
package producer

import (
	"context"
	"fmt"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// Topic is the payment.completed Kafka topic.
const Topic = "payment.completed"

// Writer is the minimal kafka.Writer surface the producer needs (satisfied by
// *kafka.Writer in production and a stub in tests).
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
}

// Producer emits PaymentEvents to Kafka.
type Producer struct {
	w Writer
}

// New constructs a Producer over the given writer.
func New(w Writer) *Producer { return &Producer{w: w} }

// NewKafkaWriter builds a production *kafka.Writer for payment.completed with
// acks=all + key-hash partitioning (events for one order stay ordered).
func NewKafkaWriter(brokers []string) *kafka.Writer {
	return &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        Topic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
	}
}

// Emit publishes a PaymentEvent. The message key is the order id so all events
// for one order land on the same partition (ordered apply downstream). A write
// error is returned so the webhook handler can 5xx (provider redelivers).
func (p *Producer) Emit(ctx context.Context, ev *paymentv1.PaymentEvent) error {
	if p == nil || p.w == nil {
		return fmt.Errorf("producer: not configured")
	}
	value, err := protojson.Marshal(ev)
	if err != nil {
		return fmt.Errorf("producer: marshal payment event: %w", err)
	}
	key := ev.GetOrderId()
	if key == "" {
		key = ev.GetExternalSubscriptionId()
	}
	return p.w.WriteMessages(ctx, kafka.Message{
		Topic: Topic,
		Key:   []byte(key),
		Value: value,
	})
}
