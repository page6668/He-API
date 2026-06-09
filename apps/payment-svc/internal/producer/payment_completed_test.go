// Story 7.3 — 7.3-CONTRACT-002 / 7.3-INT-020. The producer marshals a
// PaymentEvent with protojson (so billing-svc decodes it identically) and keys
// the message by order id for ordered, per-order delivery.
package producer

import (
	"context"
	"testing"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

type fakeWriter struct {
	msgs []kafka.Message
	err  error
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, msgs...)
	return nil
}

func TestEmit_MarshalsProtojson_KeyedByOrder(t *testing.T) {
	fw := &fakeWriter{}
	p := New(fw)
	ev := &paymentv1.PaymentEvent{
		OrderId:         "order-1",
		PaymentProvider: "stripe",
		ExternalOrderId: "pi_1",
		SettledAmount:   "50.00",
		Currency:        "USD",
		Status:          "paid",
		EventType:       "recharge_paid",
		Ts:              "2026-06-09T12:00:00Z",
	}
	if err := p.Emit(context.Background(), ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(fw.msgs) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(fw.msgs))
	}
	m := fw.msgs[0]
	if string(m.Key) != "order-1" {
		t.Errorf("key = %q, want order-1 (per-order partition affinity)", m.Key)
	}
	if m.Topic != Topic {
		t.Errorf("topic = %q, want %q", m.Topic, Topic)
	}
	// Round-trips through protojson exactly as billing-svc will decode it.
	var out paymentv1.PaymentEvent
	if err := protojson.Unmarshal(m.Value, &out); err != nil {
		t.Fatalf("protojson decode: %v", err)
	}
	if out.GetSettledAmount() != "50.00" || out.GetOrderId() != "order-1" {
		t.Errorf("decoded mismatch: %+v", &out)
	}
}
