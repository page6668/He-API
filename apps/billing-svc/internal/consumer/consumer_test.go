package consumer

import (
	"context"
	"errors"
	"testing"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/he-api/he-api/apps/billing-svc/internal/ledger"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

type fakeApplier struct {
	res ledger.Result
	err error
}

func (f fakeApplier) Apply(context.Context, *billingv1.UsageEvent) (ledger.Result, error) {
	return f.res, f.err
}

type fakeDLQ struct {
	written []kafka.Message
	err     error
}

func (f *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, msgs...)
	return nil
}

func goodMsg(t *testing.T) kafka.Message {
	t.Helper()
	b, err := protojson.Marshal(&billingv1.UsageEvent{
		LedgerKey:   "req_H1",
		HeRequestId: "req_H1",
		UserId:      "u1",
		Model:       "qwen-max",
	})
	if err != nil {
		t.Fatal(err)
	}
	return kafka.Message{Topic: Topic, Key: []byte("u1"), Value: b}
}

// 7.1-INT-001/002 — applied OR duplicate → commit the offset (ACK).
func TestHandle_AppliedCommits(t *testing.T) {
	for _, oc := range []ledger.Outcome{ledger.OutcomeApplied, ledger.OutcomeDuplicate} {
		dlq := &fakeDLQ{}
		c := New(nil, dlq, fakeApplier{res: ledger.Result{Outcome: oc}}, nil)
		commit, err := c.handle(context.Background(), goodMsg(t))
		if err != nil || !commit {
			t.Fatalf("outcome %v: commit=%v err=%v, want commit=true err=nil", oc, commit, err)
		}
		if len(dlq.written) != 0 {
			t.Fatalf("outcome %v: must not DLQ", oc)
		}
	}
}

// 7.1-INT-005 — PG fault on Apply → retain the offset (no commit), no DLQ.
func TestHandle_PGFaultRetainsOffset(t *testing.T) {
	dlq := &fakeDLQ{}
	c := New(nil, dlq, fakeApplier{err: errors.New("pg down")}, nil)
	commit, err := c.handle(context.Background(), goodMsg(t))
	if err == nil {
		t.Fatal("expected error to retain offset")
	}
	if commit {
		t.Fatal("must NOT commit on PG fault")
	}
	if len(dlq.written) != 0 {
		t.Fatal("PG fault must NOT dead-letter (it is retried, not parked)")
	}
}

// 7.1-INT-009 — ErrNoPricing → park to DLQ + commit (no charge, event not lost).
func TestHandle_NoPricingDeadLetters(t *testing.T) {
	dlq := &fakeDLQ{}
	c := New(nil, dlq, fakeApplier{res: ledger.Result{Outcome: ledger.OutcomeNoPricing}}, nil)
	commit, err := c.handle(context.Background(), goodMsg(t))
	if err != nil || !commit {
		t.Fatalf("commit=%v err=%v, want commit=true err=nil", commit, err)
	}
	if len(dlq.written) != 1 || dlq.written[0].Topic != DLQTopic {
		t.Fatalf("expected one DLQ message on topic %s, got %+v", DLQTopic, dlq.written)
	}
	if reason := headerVal(dlq.written[0], "x-dlq-reason"); reason != "no_pricing" {
		t.Fatalf("dlq reason = %q, want no_pricing", reason)
	}
}

// 7.1-BLIND-ERROR-002 — malformed event → park to DLQ + commit, no panic.
func TestHandle_MalformedDeadLetters(t *testing.T) {
	dlq := &fakeDLQ{}
	c := New(nil, dlq, fakeApplier{res: ledger.Result{Outcome: ledger.OutcomeApplied}}, nil)
	msg := kafka.Message{Topic: Topic, Value: []byte("{not valid json")}
	commit, err := c.handle(context.Background(), msg)
	if err != nil || !commit {
		t.Fatalf("commit=%v err=%v, want commit=true err=nil", commit, err)
	}
	if len(dlq.written) != 1 || headerVal(dlq.written[0], "x-dlq-reason") != "malformed" {
		t.Fatalf("expected malformed DLQ park, got %+v", dlq.written)
	}
}

// 7.1-BLIND-ERROR-003 — DLQ topic itself unavailable → do NOT commit (retain
// offset, no silent loss).
func TestHandle_DLQDownRetainsOffset(t *testing.T) {
	dlq := &fakeDLQ{err: errors.New("dlq broker down")}
	c := New(nil, dlq, fakeApplier{res: ledger.Result{Outcome: ledger.OutcomeNoPricing}}, nil)
	commit, err := c.handle(context.Background(), goodMsg(t))
	if err == nil {
		t.Fatal("expected error when DLQ write fails")
	}
	if commit {
		t.Fatal("must NOT commit when the DLQ park failed (no silent loss)")
	}
}

func headerVal(m kafka.Message, key string) string {
	for _, h := range m.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
