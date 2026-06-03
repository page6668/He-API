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
	EventSignup                 EventType = "auth.signup"
	EventSignupDuplicateAttempt EventType = "auth.signup_duplicate_attempt"
	EventVerifyEmail            EventType = "auth.verify_email"
	EventVerifyEmailBruteForce  EventType = "auth.verify_email_brute_force"
	EventSigninSuccess          EventType = "auth.signin_success"
	EventSigninFailure          EventType = "auth.signin_failure"
	EventAccountLocked          EventType = "auth.account_locked"
	EventEmailSendFailed        EventType = "auth.email_send_failed"

	// Story 2.3 OAuth event types (12 total per BR-4.3). All carry
	// hashed PII only. The set replaces the earlier 9-event collapsed
	// taxonomy after QA review round 1 split rejected_conflict into the
	// two distinct B.4 + B.5 reasons and added the missing suspended
	// rejection path. The 13th canonical type — invalid_return_to —
	// is emitted as a structured warn log from api-gateway because the
	// rejection happens pre-Redis and api-gateway has no Kafka publisher
	// in Story 2.3 scope (see apps/api-gateway/internal/handlers/oauth.go).
	EventOAuthInitiate               EventType = "auth.oauth.initiate"
	EventOAuthCallbackSuccess        EventType = "auth.oauth.callback.success"
	EventOAuthCallbackErrState       EventType = "auth.oauth.callback.error_state"
	EventOAuthCallbackErrProv        EventType = "auth.oauth.callback.error_provider"
	EventOAuthCallbackErrBinding     EventType = "auth.oauth.callback.error_binding"
	EventOAuthCallbackRejSuspended   EventType = "auth.oauth.callback.rejected_suspended"
	EventOAuthLinkSuccess            EventType = "auth.oauth.link.success"
	EventOAuthLinkRejUnverified      EventType = "auth.oauth.link.rejected_unverified"
	EventOAuthLinkRejSubjectMismatch EventType = "auth.oauth.link.rejected_subject_mismatch"
	EventOAuthLinkRejOtherProvider   EventType = "auth.oauth.link.rejected_other_provider"
	EventOAuthLockBypass             EventType = "auth.oauth.lock_bypass"
	EventOAuthInvalidReturnTo        EventType = "auth.oauth.invalid_return_to"

	// Story 2.4 — 2FA event taxonomy (10 types per BR-5.7). Severity is
	// recorded in Event.Metadata["severity"] = LOW/MEDIUM/HIGH per BR-5.7
	// mapping so audit-svc routing keeps a single envelope shape.
	Event2FAEnrollInitiated     EventType = "auth.2fa.enroll.initiated"         // LOW
	Event2FAEnrolled            EventType = "auth.2fa.enrolled"                 // MEDIUM
	Event2FAEnrollFailed        EventType = "auth.2fa.enroll.failed"            // LOW
	Event2FAChallengeSuccess    EventType = "auth.2fa.challenge.success"        // LOW
	Event2FAChallengeFailed     EventType = "auth.2fa.challenge.failed"         // LOW
	Event2FAChallengeLocked     EventType = "auth.2fa.challenge.locked"         // HIGH — possible attack
	Event2FAChallengeBinding    EventType = "auth.2fa.challenge.binding_failed" // HIGH — possible cookie theft
	Event2FARecoveryUsed        EventType = "auth.2fa.recovery.used"            // HIGH — out-of-band event
	Event2FARecoveryRegenerated EventType = "auth.2fa.recovery.regenerated"     // MEDIUM
	Event2FADisabled            EventType = "auth.2fa.disabled"                 // HIGH — security downgrade

	// Story 2.5 — profile mutation event. Severity LOW; emitted on every
	// successful PUT /v1/me/profile (display_name + locale + timezone).
	// Diff payload uses BR-2.10 redaction (display_name <set>/<cleared>;
	// locale/timezone literal). Required for GDPR data-export (Story 2.6).
	EventProfileUpdated EventType = "profile.updated" // LOW

	// Story 2.6 — GDPR data-export taxonomy (3 types per AC6 BR-6.6). The
	// EVENTS THEMSELVES are emitted by notification-svc and analytics-svc
	// (Accumulated Context table: "REUSE-OF-PATTERN — each service
	// instantiates its own publisher with the same contract"). auth-svc
	// only declares the constants here so the taxonomy stays counted by a
	// single grep — TS-CONS-004 verifies "31 pre-2.6 → 34 post-2.6" via
	// apps/auth-svc/internal/audit/audit.go.
	//
	// Severity per BR-6.6:
	//   - requested / completed → INFO/LOW
	//   - failed (stage ∈ pg_dump / ch_dump / oss_upload / oss_sign_url /
	//     timeout) → WARN
	//   - failed (stage = email_send) → ERROR (pages ops via PagerDuty —
	//     user can't retrieve their data)
	EventGdprExportRequested EventType = "gdpr.export.requested" // LOW
	EventGdprExportCompleted EventType = "gdpr.export.completed" // LOW
	EventGdprExportFailed    EventType = "gdpr.export.failed"    // WARN or ERROR per failed_stage

	// Story 5.1 — API Key management events (BR-3.5 + BR-3.6). Severity LOW
	// for created; MEDIUM for revoked (credential lifecycle event — ops
	// SHOULD be able to grep revocation history for compromise forensics).
	// Payload (per BR-3.5/3.6): {audit_event_type, api_key_id, user_id,
	// key_prefix, name, client_ip_hash, user_agent_hash, ts[, revoked_at]}.
	// The Metadata map carries api_key_id, key_prefix, name (and revoked_at
	// for the revoke variant); IP + UserAgent fields carry the SHA-256
	// hashes (NOT plaintext — verified by SECURITY-006 grep sweep).
	// Plaintext key + key_hash are NEVER part of the payload.
	EventAPIKeyCreated EventType = "api_key.created" // LOW
	EventAPIKeyRevoked EventType = "api_key.revoked" // MEDIUM
	// EventAPIKeyConfigUpdated (Story 5.2 BR-1.11) carries the same PII-safe
	// payload shape PLUS a `changed_fields` []string in Metadata enumerating
	// the top-level paths that mutated (e.g. ["scope.ip_whitelist",
	// "monthly_cost_cap_usd"]) for compliance lineage. NEVER plaintext / hash
	// of the key. Distinct event_type so audit-svc routes by type with no
	// consumer code change.
	EventAPIKeyConfigUpdated EventType = "api_key.config_updated" // LOW
)

// Severity classification for the 10 Story 2.4 event types per BR-5.7.
// audit-svc downstream consumers use this to route HIGH events to ops
// alerting + ClickHouse-with-retention bucket separation.
const (
	SeverityLow    = "LOW"
	SeverityMedium = "MEDIUM"
	SeverityHigh   = "HIGH"
)

// Severity2FA returns the canonical severity for a 2FA event type. Returns
// SeverityLow for non-2FA event types (the legacy Story 2.2 / 2.3 taxonomy
// uses a flatter convention — see audit-svc consumer for those mappings).
func Severity2FA(t EventType) string {
	switch t {
	case Event2FAChallengeLocked, Event2FAChallengeBinding,
		Event2FARecoveryUsed, Event2FADisabled:
		return SeverityHigh
	case Event2FAEnrolled, Event2FARecoveryRegenerated:
		return SeverityMedium
	case Event2FAEnrollInitiated, Event2FAEnrollFailed,
		Event2FAChallengeSuccess, Event2FAChallengeFailed:
		return SeverityLow
	default:
		return SeverityLow
	}
}

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
	EventType EventType      `json:"event_type"`
	UserID    string         `json:"user_id,omitempty"`
	EmailHash string         `json:"email_hash"`
	IP        string         `json:"ip,omitempty"`
	UserAgent string         `json:"ua,omitempty"`
	Timestamp time.Time      `json:"ts"`
	Success   bool           `json:"success"`
	ErrorCode string         `json:"error_code,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
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
		logger.WarnContext(
			ctx, "audit publish failed",
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
	p.Logger.InfoContext(
		ctx, "audit",
		slog.String("event_type", string(event.EventType)),
		slog.String("email_hash", event.EmailHash),
		slog.String("user_id", event.UserID),
		slog.Bool("success", event.Success),
		slog.String("error_code", event.ErrorCode),
	)
	return nil
}
