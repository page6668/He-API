// KafkaPublisher unit tests — UNIT-181 + BR-4.5 schema + BR-4.5 partition
// key invariant.
//
// Tests use a fake KafkaWriter so the suite runs without a broker. The
// real-broker integration test is INT-064 / INT-065 in P6 (testcontainers).

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

// fakeKafkaWriter captures messages + returns a configurable error from
// WriteMessages. Records the most recent ctx so tests can verify
// cancellation semantics if needed.
type fakeKafkaWriter struct {
	err      error
	captured []kafka.Message
}

func (f *fakeKafkaWriter) WriteMessages(ctx context.Context, msgs ...kafka.Message) error {
	if f.err != nil {
		return f.err
	}
	f.captured = append(f.captured, msgs...)
	return nil
}

// --- BR-4.5 schema + partition key ---

func TestKafkaPublisher_MarshalsBR45Schema(t *testing.T) {
	w := &fakeKafkaWriter{}
	p := NewKafkaPublisher(w, slog.Default())

	ts := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC)
	event := Event{
		EventType: EventSignup,
		UserID:    "9c8e6f3a-1234-5678-90ab-cdef01234567",
		EmailHash: "abcdef0123456789",
		IP:        "10.0.0.42",
		UserAgent: "Mozilla/5.0",
		Timestamp: ts,
		Success:   true,
		ErrorCode: "",
	}

	if err := p.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(w.captured) != 1 {
		t.Fatalf("captured msg count: got %d, want 1", len(w.captured))
	}
	msg := w.captured[0]

	// BR-4.5 partition key invariant.
	if string(msg.Key) != event.EmailHash {
		t.Errorf("partition key: got %q, want %q (BR-4.5 — per-account ordering)", string(msg.Key), event.EmailHash)
	}

	// BR-4.5 schema round-trip.
	var got Event
	if err := json.Unmarshal(msg.Value, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EventType != EventSignup {
		t.Errorf("event_type: got %q, want %q", got.EventType, EventSignup)
	}
	if got.UserID != event.UserID {
		t.Errorf("user_id mismatch: got %q, want %q", got.UserID, event.UserID)
	}
	if got.EmailHash != event.EmailHash {
		t.Errorf("email_hash mismatch: got %q, want %q", got.EmailHash, event.EmailHash)
	}
	if !got.Timestamp.Equal(ts) {
		t.Errorf("ts mismatch: got %v, want %v", got.Timestamp, ts)
	}
	if !got.Success {
		t.Errorf("success: got false, want true")
	}

	// Message Time MUST mirror event.Timestamp so Kafka's own
	// time-window indexing aligns with the audit event semantics.
	if !msg.Time.Equal(ts) {
		t.Errorf("kafka msg time: got %v, want %v", msg.Time, ts)
	}
}

func TestKafkaPublisher_NoPlaintextEmail(t *testing.T) {
	// TS-CONS-005 guard via the wire payload: no raw email or password
	// fields. The Event struct doesn't accept them, but verify the
	// marshaled JSON also doesn't accidentally serialize them under
	// any future field tag drift.
	w := &fakeKafkaWriter{}
	p := NewKafkaPublisher(w, slog.Default())

	event := Event{
		EventType: EventSignup,
		EmailHash: "abc",
		Timestamp: time.Now(),
		Success:   true,
	}
	if err := p.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	body := string(w.captured[0].Value)
	for _, banned := range []string{`"email"`, `"password"`, `"plaintext"`, `"token"`} {
		if strings.Contains(body, banned) {
			t.Errorf("audit payload must not contain %s field: %s", banned, body)
		}
	}
}

// --- UNIT-181: Kafka down → Publish returns error; PublishBestEffort swallows + warn-logs ---

func TestKafkaPublisher_PropagatesWriteError(t *testing.T) {
	wantErr := errors.New("broker unreachable")
	w := &fakeKafkaWriter{err: wantErr}
	p := NewKafkaPublisher(w, slog.Default())

	err := p.Publish(context.Background(), Event{
		EventType: EventSigninFailure,
		EmailHash: "abc",
		Timestamp: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error from underlying writer; got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error chain: got %v, want %v in chain", err, wantErr)
	}
}

func TestPublishBestEffort_SwallowsKafkaError_AndWarnLogs(t *testing.T) {
	// Capture the warn log line emitted by PublishBestEffort to verify
	// the UNIT-181 "warn log + Sentry" contract. (Sentry plumbing is
	// done via the slog handler in obs.NewLogger — Story 1.4 wired the
	// hook. The audit-level contract is "we warn-log when audit fails",
	// not "we hand the log to Sentry directly".)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	w := &fakeKafkaWriter{err: errors.New("broker down")}
	p := NewKafkaPublisher(w, logger)

	// PublishBestEffort is the call site contract used by every
	// handler. It MUST NOT propagate the error.
	PublishBestEffort(context.Background(), p, logger, Event{
		EventType: EventSigninFailure,
		EmailHash: "abc",
		Timestamp: time.Now(),
		ErrorCode: "401_invalid_credentials",
	})

	logOut := buf.String()
	if !strings.Contains(logOut, "audit publish failed") {
		t.Errorf("expected warn log line 'audit publish failed' in: %s", logOut)
	}
	if !strings.Contains(logOut, "auth.signin_failure") {
		t.Errorf("expected event_type in warn log: %s", logOut)
	}
	if !strings.Contains(logOut, "401_invalid_credentials") {
		t.Errorf("expected error_code in warn log: %s", logOut)
	}
	// Level check via the JSON envelope.
	if !strings.Contains(logOut, `"level":"WARN"`) {
		t.Errorf("log level must be WARN: %s", logOut)
	}
}

func TestKafkaPublisher_NilLoggerDefaultsToSlogDefault(t *testing.T) {
	// Constructor must not panic on nil logger; production wiring in
	// cmd/server always provides one, but defensive coverage prevents
	// regression.
	w := &fakeKafkaWriter{}
	p := NewKafkaPublisher(w, nil)
	if p.logger == nil {
		t.Fatal("nil logger should default to slog.Default(), got nil")
	}
}
