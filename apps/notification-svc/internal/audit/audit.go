// Package audit captures domain events from the notification flow and
// publishes them to the Kafka topic `audit.event` (Story 2.2 TS-CONS-015 —
// reusing the existing topic; Story 2.6 extends the event taxonomy with 3
// GDPR-domain event types).
//
// Mirrors the contract documented in apps/auth-svc/internal/audit (Story 2.2
// deliverable) — each service has its own audit publisher instance against
// the same topic, sharing the wire payload (Event struct below). The
// duplication is intentional: each app is a separate Go module, and the
// taxonomy is a documentation-level invariant (per Architect-verified
// count "31 pre-2.6 → 34 post-2.6"), not a code-level invariant.
//
// Publish failure is best-effort (TS-CONS-005): callers invoke
// PublishBestEffort which warn-logs on failure and never blocks the
// business path.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// EventType enumerates the audit event types emitted by notification-svc.
// Story 2.6 adds the 3 GDPR-domain event types (post-2.5 count 31 → post-
// 2.6 count 34, per Story 2.6 TS-CONS-004 + Architect Round 2 verification
// of apps/auth-svc/internal/audit/audit.go).
type EventType string

const (
	// Story 2.6 — emitted by RequestDataExport handler after PG insert +
	// gdpr.export.requested Kafka publish (AC2 step 4). Severity LOW.
	EventGdprExportRequested EventType = "gdpr.export.requested"
	// Story 2.6 — emitted by notification-svc when an email-send retry
	// chain exhausts and the data is in OSS but the user can't retrieve
	// the link (AC5 BR-5.6; AC6 BR-6.6 ERROR severity — pages ops).
	// analytics-svc emits its own `gdpr.export.failed` for upstream
	// failure stages (pg_dump, ch_dump, oss_upload, oss_sign_url, timeout).
	EventGdprExportFailed EventType = "gdpr.export.failed"
)

// Severity classification per AC6 BR-6.6.
const (
	SeverityLow    = "LOW"
	SeverityMedium = "MEDIUM"
	SeverityHigh   = "HIGH"
	SeverityError  = "ERROR"
)

// Event is the BR-4.5 wire-shape consumed by audit-svc → ClickHouse. Field
// tags use snake_case to match data-models.md §4.4. Mirrors auth-svc's Event
// (Story 2.2 TS-CONS-015) so the consumer can deserialize uniformly.
//
// AC6 BR-6.5 PII guard: the payload MUST NOT contain email, display_name,
// or user-supplied IP. Server-derived IP (api-gateway middleware) IS
// allowed in the IP field. The dump content is NEVER referenced here.
type Event struct {
	EventType EventType      `json:"event_type"`
	UserID    string         `json:"user_id,omitempty"`
	EmailHash string         `json:"email_hash,omitempty"`
	IP        string         `json:"ip,omitempty"`
	UserAgent string         `json:"ua,omitempty"`
	Timestamp time.Time      `json:"ts"`
	Success   bool           `json:"success"`
	ErrorCode string         `json:"error_code,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// MarshalJSON exists so a future Kafka producer can hand the event directly
// to the wire codec; matches auth-svc's hook for Avro/Protobuf swaps.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return json.Marshal(alias(e))
}

// Publisher dispatches an audit event.
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

// PublishBestEffort wraps Publish with TS-CONS-005 "never block business"
// semantics. Returns nil always; failures warn-log only.
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

// NoOpPublisher emits the event as a structured log line. Used until the
// Kafka writer is wired in cmd/server (matches the auth-svc P2f pattern).
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
		slog.String("user_id", event.UserID),
		slog.Bool("success", event.Success),
		slog.String("error_code", event.ErrorCode),
	)
	return nil
}
