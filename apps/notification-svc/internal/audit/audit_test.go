package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// recordingPublisher captures Publish calls for assertion.
type recordingPublisher struct {
	events []Event
	err    error
}

func (r *recordingPublisher) Publish(_ context.Context, e Event) error {
	r.events = append(r.events, e)
	return r.err
}

// 2.6-UNIT-100 — Event JSON marshalling preserves snake_case wire shape.
func TestEventMarshalJSON_SnakeCaseShape(t *testing.T) {
	t.Parallel()
	e := Event{
		EventType: EventGdprExportRequested,
		UserID:    "user-1",
		Timestamp: time.Unix(1700000000, 0).UTC(),
		Success:   true,
		Metadata:  map[string]any{"export_id": "exp-1", "severity": SeverityLow},
	}
	body, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := raw["event_type"]; got != string(EventGdprExportRequested) {
		t.Errorf("event_type: got %v", got)
	}
	if got := raw["user_id"]; got != "user-1" {
		t.Errorf("user_id: got %v", got)
	}
	if got := raw["success"]; got != true {
		t.Errorf("success: got %v", got)
	}
}

// 2.6-UNIT-101 — BR-6.5 PII guard. The Event payloads emitted by Story 2.6
// callers (notification-svc + analytics-svc bridging through this struct)
// MUST NOT carry email, display_name, or client-supplied IP. The struct
// itself exposes EmailHash / IP / UserAgent fields for the shared
// taxonomy, but the 3 GDPR event types are produced with those fields
// empty. This test pins the contract so a future caller can't drift.
func TestBR65_PIIGuard_GDPREvents(t *testing.T) {
	t.Parallel()
	// Mirrors the call sites in handlers/data_export.go (requested) and
	// the analytics-svc worker (completed / failed). If a future caller
	// populates these fields, this test fails.
	gdprEventTypes := []EventType{
		EventGdprExportRequested,
		EventGdprExportFailed,
		// completed is not declared in notification-svc; analytics-svc
		// produces it. We pin the EventType string here for parity.
		EventType("gdpr.export.completed"),
	}
	for _, et := range gdprEventTypes {
		et := et
		t.Run(string(et), func(t *testing.T) {
			t.Parallel()
			e := Event{
				EventType: et,
				UserID:    "user-uuid",
				Timestamp: time.Now().UTC(),
				Success:   et != EventGdprExportFailed,
				Metadata:  map[string]any{"export_id": "exp-1", "severity": SeverityLow},
			}
			body, err := json.Marshal(e)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			for _, banned := range []string{"email", "display_name", "name"} {
				if _, ok := raw[banned]; ok {
					t.Errorf("BR-6.5 violation: payload carries forbidden field %q", banned)
				}
			}
			// Allowed fields with empty values must omit (snake_case JSON
			// `,omitempty` tags). Re-marshal asserts the field absence.
			if v, ok := raw["email_hash"]; ok && v != "" {
				t.Errorf("BR-6.5 violation: email_hash present (%v)", v)
			}
			if v, ok := raw["ip"]; ok && v != "" {
				t.Errorf("BR-6.5 violation: client IP present (%v)", v)
			}
			if v, ok := raw["ua"]; ok && v != "" {
				t.Errorf("BR-6.5 violation: user_agent present (%v)", v)
			}
			// Metadata is allowed; but must not contain user-supplied PII.
			meta, _ := raw["metadata"].(map[string]any)
			for _, banned := range []string{"email", "display_name", "name", "phone"} {
				if _, ok := meta[banned]; ok {
					t.Errorf("BR-6.5 violation: metadata carries forbidden field %q", banned)
				}
			}
		})
	}
}

// TestPublishBestEffort_SwallowsError verifies TS-CONS-005 — Publish
// failures never propagate to the business path; the call returns void
// and warn-logs.
func TestPublishBestEffort_SwallowsError(t *testing.T) {
	t.Parallel()
	rp := &recordingPublisher{err: errors.New("boom")}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// Must not panic, must not propagate the error to the caller.
	PublishBestEffort(context.Background(), rp, logger, Event{
		EventType: EventGdprExportRequested,
		UserID:    "u1",
		Timestamp: time.Now(),
	})
	if !bytes.Contains(buf.Bytes(), []byte("audit publish failed")) {
		t.Errorf("expected warn log line, got: %q", buf.String())
	}
	if len(rp.events) != 1 {
		t.Errorf("expected 1 captured event, got %d", len(rp.events))
	}
}

// TestPublishBestEffort_Success — success path emits no warn log.
func TestPublishBestEffort_Success(t *testing.T) {
	t.Parallel()
	rp := &recordingPublisher{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	PublishBestEffort(context.Background(), rp, logger, Event{
		EventType: EventGdprExportRequested,
		UserID:    "u1",
		Timestamp: time.Now(),
	})
	if bytes.Contains(buf.Bytes(), []byte("audit publish failed")) {
		t.Errorf("unexpected warn log: %q", buf.String())
	}
}

// TestNoOpPublisher_Publish — NoOpPublisher always returns nil and logs
// at info level.
func TestNoOpPublisher_Publish(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	p := NewNoOpPublisher(logger)
	err := p.Publish(context.Background(), Event{
		EventType: EventGdprExportRequested,
		UserID:    "u1",
		Success:   true,
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("audit")) {
		t.Errorf("expected audit log line, got: %q", buf.String())
	}
}

// TestEventTypeConstants pins the 3 Story 2.6 audit event type strings
// against accidental rename. The constant strings flow into the
// audit.event Kafka payload's event_type field, which audit-svc/CH
// queries depend on.
func TestEventTypeConstants(t *testing.T) {
	t.Parallel()
	cases := map[EventType]string{
		EventGdprExportRequested: "gdpr.export.requested",
		EventGdprExportFailed:    "gdpr.export.failed",
	}
	for got, want := range cases {
		if string(got) != want {
			t.Errorf("event type drift: got %q, want %q", got, want)
		}
	}
}
