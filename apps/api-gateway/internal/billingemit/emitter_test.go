package billingemit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

type fakeWriter struct {
	mu   sync.Mutex
	msgs []kafka.Message
	err  error
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, msgs...)
	return nil
}

func (f *fakeWriter) written() []kafka.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]kafka.Message(nil), f.msgs...)
}

func emitAndWait(t *testing.T, w MessageWriter, ev *billingv1.UsageEvent) {
	t.Helper()
	e := NewKafkaEmitter(w, nil)
	done := make(chan struct{})
	e.onDone = func() { close(done) }
	e.Emit(context.Background(), ev)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit goroutine did not complete")
	}
}

func sampleEvent() *billingv1.UsageEvent {
	return &billingv1.UsageEvent{
		LedgerKey:        "req_H1",
		HeRequestId:      "req_H1",
		UserId:           "u1",
		ApiKeyId:         "k1",
		Model:            "qwen-max",
		PromptTokens:     1500,
		CompletionTokens: 800,
		TotalTokens:      2300,
		BillingMode:      billingv1.BillingMode_BILLING_MODE_PER_TOKEN,
	}
}

// 7.1-INT-014 / INT-019 — one event written, keyed by user_id, carrying RAW
// inputs and NO cost field (the JSON must not contain "cost").
func TestEmit_RawEventKeyedByUser(t *testing.T) {
	w := &fakeWriter{}
	emitAndWait(t, w, sampleEvent())

	msgs := w.written()
	if len(msgs) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(msgs))
	}
	if msgs[0].Topic != Topic {
		t.Fatalf("topic = %q, want %q", msgs[0].Topic, Topic)
	}
	if string(msgs[0].Key) != "u1" {
		t.Fatalf("key = %q, want u1 (partition stability)", string(msgs[0].Key))
	}
	// Q-CH — the event carries NO cost; billing-svc is the sole cost authority.
	if body := string(msgs[0].Value); contains(body, "cost") {
		t.Fatalf("usage event must NOT carry cost (Q-CH); body=%s", body)
	}
}

// 7.1-INT-013 — fire-and-forget: a writer error is swallowed (no panic, Emit
// returns normally; the response is unaffected because emit is detached).
func TestEmit_FireAndForget_WriteError(t *testing.T) {
	w := &fakeWriter{err: errors.New("broker down")}
	// Must not panic and must complete the detached goroutine.
	emitAndWait(t, w, sampleEvent())
}

// Nop emitter never writes.
func TestNop(t *testing.T) {
	Nop{}.Emit(context.Background(), sampleEvent()) // no panic, no-op
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
