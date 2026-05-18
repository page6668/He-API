package events

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
	"google.golang.org/protobuf/proto"

	gdprv1 "github.com/he-api/he-api/packages/proto/gen/go/he/gdpr/v1"
)

// fakeWriter captures WriteMessages calls.
type fakeWriter struct {
	msgs    []kafka.Message
	failErr error
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.msgs = append(f.msgs, msgs...)
	return nil
}

// 2.6-UNIT-060 — Happy path: Publish marshals proto DataExportRequestedEvent
// to the gdpr.export.requested topic with key=user_id (BR-6.1 partition
// stability invariant).
func TestPublish_HappyPath(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	p := NewGDPRExportPublisher(w, slog.Default())
	requestedAt := time.Unix(1700000000, 0).UTC()
	if err := p.Publish(context.Background(), "exp-1", "user-1", requestedAt); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(w.msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(w.msgs))
	}
	m := w.msgs[0]
	if m.Topic != TopicGDPRExportRequested {
		t.Errorf("topic: got %q, want %q", m.Topic, TopicGDPRExportRequested)
	}
	if string(m.Key) != "user-1" {
		t.Errorf("key: got %q, want user-1 (BR-6.1 partition key)", string(m.Key))
	}
	if !m.Time.Equal(requestedAt) {
		t.Errorf("time: got %v, want %v", m.Time, requestedAt)
	}
	// Verify proto round-trip
	got := &gdprv1.DataExportRequestedEvent{}
	if err := proto.Unmarshal(m.Value, got); err != nil {
		t.Fatalf("proto unmarshal: %v", err)
	}
	if got.GetExportId() != "exp-1" || got.GetUserId() != "user-1" {
		t.Errorf("proto payload: got %v", got)
	}
	if !got.GetRequestedAt().AsTime().Equal(requestedAt) {
		t.Errorf("requested_at proto: got %v, want %v", got.GetRequestedAt().AsTime(), requestedAt)
	}
}

// 2.6-UNIT-061 — Publish on a nil-writer publisher returns a stable error
// rather than panicking.
func TestPublish_NilPublisher(t *testing.T) {
	t.Parallel()
	var p *GDPRExportPublisher
	err := p.Publish(context.Background(), "exp-1", "user-1", time.Now())
	if err == nil {
		t.Fatal("expected error on nil publisher")
	}
}

// 2.6-UNIT-062 — required-arg validation.
func TestPublish_RequiredArgs(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{}
	p := NewGDPRExportPublisher(w, slog.Default())
	if err := p.Publish(context.Background(), "", "u1", time.Now()); err == nil {
		t.Error("expected error on empty export_id")
	}
	if err := p.Publish(context.Background(), "e1", "", time.Now()); err == nil {
		t.Error("expected error on empty user_id")
	}
}

// 2.6-UNIT-063 — Writer error propagates to the caller (the surrounding
// handler decides whether to rollback the PG tx).
func TestPublish_WriterErrorPropagates(t *testing.T) {
	t.Parallel()
	w := &fakeWriter{failErr: errors.New("kafka down")}
	p := NewGDPRExportPublisher(w, slog.Default())
	err := p.Publish(context.Background(), "e1", "u1", time.Now())
	if err == nil || err.Error() != "kafka down" {
		t.Errorf("expected writer error to propagate, got %v", err)
	}
}

// 2.6-UNIT-064 — Topic constant pinning. The Terraform module
// `infra/terraform/modules/kafka-topics` declares this exact name; a
// rename here without a corresponding infra change would create a
// non-functional consumer pair.
func TestTopicConstant(t *testing.T) {
	t.Parallel()
	if TopicGDPRExportRequested != "gdpr.export.requested" {
		t.Errorf("topic name drift: got %q", TopicGDPRExportRequested)
	}
}
