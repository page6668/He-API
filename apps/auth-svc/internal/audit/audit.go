// Package audit captures domain events from the auth flow and publishes them
// to the Kafka topic `audit.event` (BR-4.5 + TS-CONS-015). For Story 2.2 P2f
// only the `NoOpPublisher` stub is wired — full Kafka producer integration
// lands in P5/T4 alongside the cross-cutting metrics + alerting work.
//
// Publish failure MUST NOT block the business path (BR-4.5 + TS-CONS-009):
// callers invoke `PublishBestEffort` which logs at warn level on failure
// but always returns nil.
//
// Payload invariants (TS-CONS-005):
//   - email_hash NEVER carries plaintext email; the value is the same
//     SHA-256 hash used as ratelimit key suffix (BR-4.2).
//   - password / token plaintext fields are absent from the struct entirely;
//     no field can accept them by accident.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// EventType enumerates the 8 audit event types Story 2.2 introduces
// (TS-CONS-015 + BR-4.5).
type EventType string

const (
	EventSignup                  EventType = "auth.signup"
	EventSignupDuplicateAttempt  EventType = "auth.signup_duplicate_attempt"
	EventVerifyEmail             EventType = "auth.verify_email"
	EventVerifyEmailBruteForce   EventType = "auth.verify_email_brute_force"
	EventSigninSuccess           EventType = "auth.signin_success"
	EventSigninFailure           EventType = "auth.signin_failure"
	EventAccountLocked           EventType = "auth.account_locked"
	EventEmailSendFailed         EventType = "auth.email_send_failed"
)

// Event is the BR-4.5 wire-shape consumed by ClickHouse downstream
// (audit-svc → topic `audit.event` → ClickHouse `request_logs`).
//
// Field tags use snake_case to match the data-models.md §4.4 schema. omitempty
// on optional fields keeps the wire payload compact for high-throughput
// auth events.
//
// Note the absence of:
//   - any Email field (plaintext); use EmailHash.
//   - any Password / Token field; those NEVER leave their own packages.
type Event struct {
	EventType EventType              `json:"event_type"`
	UserID    string                 `json:"user_id,omitempty"`
	EmailHash string                 `json:"email_hash"`
	IP        string                 `json:"ip,omitempty"`
	UserAgent string                 `json:"ua,omitempty"`
	Timestamp time.Time              `json:"ts"`
	Success   bool                   `json:"success"`
	ErrorCode string                 `json:"error_code,omitempty"`
	Metadata  map[string]any         `json:"metadata,omitempty"`
}

// MarshalJSON exists so a future Kafka producer can hand it the event
// directly. Standard json.Marshal works on the struct already; this is a
// hook for future codec swaps (Avro, Protobuf) without changing call sites.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return json.Marshal(alias(e))
}

// Publisher dispatches an audit event. Implementations:
//   - NoOpPublisher (P2f scaffolding; logs at info)
//   - KafkaPublisher (P5/T4)
//   - in-memory fake (tests)
type Publisher interface {
	// Publish writes the event. On failure the caller MUST decide whether
	// to retry or swallow — usually swallow per TS-CONS-009.
	Publish(ctx context.Context, event Event) error
}

// PublishBestEffort wraps Publish with the TS-CONS-009 "never block business"
// semantics. Any error is logged at warn level (with the event_type and
// error_code) and discarded. Returns nil always.
//
// All RegisterUser/VerifyEmail/LoginUser handler call sites SHOULD use this
// helper unless they have a specific reason to surface audit failures
// (none currently).
func PublishBestEffort(ctx context.Context, p Publisher, logger *slog.Logger, event Event) {
	if err := p.Publish(ctx, event); err != nil {
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(ctx, "audit publish failed",
			slog.String("event_type", string(event.EventType)),
			slog.String("error_code", event.ErrorCode),
			slog.String("error", err.Error()),
		)
	}
}

// NoOpPublisher emits the event as a structured log line. Story 2.2 P2f-P4
// uses this; P5/T4 swaps in the Kafka-backed implementation without changing
// any handler call sites (Publisher interface is what's threaded).
type NoOpPublisher struct {
	Logger *slog.Logger
}

// NewNoOpPublisher returns a publisher backed by the supplied logger
// (defaults to slog.Default() when nil).
func NewNoOpPublisher(logger *slog.Logger) *NoOpPublisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &NoOpPublisher{Logger: logger}
}

// Publish logs the event at info level. Never returns an error.
func (p *NoOpPublisher) Publish(ctx context.Context, event Event) error {
	p.Logger.InfoContext(ctx, "audit",
		slog.String("event_type", string(event.EventType)),
		slog.String("email_hash", event.EmailHash),
		slog.String("user_id", event.UserID),
		slog.Bool("success", event.Success),
		slog.String("error_code", event.ErrorCode),
	)
	return nil
}
