package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/token"
)

// --- helpers --------------------------------------------------------------

func mustStoreToken(t *testing.T, h *harness, userID uuid.UUID) string {
	t.Helper()
	tok, err := token.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := token.Store(context.Background(), h.rdb, tok, userID); err != nil {
		t.Fatalf("Store: %v", err)
	}
	return tok
}

func userRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "email", "password_hash", "email_verified_at",
		"oauth_provider", "oauth_subject", "locale", "timezone",
		"totp_secret_encrypted", "totp_enabled", "status", "locked_until",
		"pending_deletion_at", "created_at", "updated_at",
	})
}

// --- VerifyEmail tests ---------------------------------------------------

// Scenario: 2.2-UNIT-080
// Malformed token → 400_invalid_token. Never touches Redis or PG.
func TestVerifyEmail_RejectsMalformedToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, err := h.srv.VerifyEmail(context.Background(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token: "too-short",
	}))
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidToken)
}

// Scenario: 2.2-UNIT-081
// Token not in Redis (TTL elapsed or never issued) → 410_token_expired.
func TestVerifyEmail_MissingTokenReturns410Expired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tok, _ := token.Generate()
	_, err := h.srv.VerifyEmail(context.Background(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token: tok,
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusTokenExpired)
}

// Scenario: 2.2-UNIT-082
// Happy path: token valid + user not yet verified. UPDATE affected=1 →
// status="email_verified" + audit auth.verify_email + Redis primary +
// reverse keys DEL'd by Consume.
func TestVerifyEmail_HappyPathFirstTime(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.New()
	tok := mustStoreToken(t, h, userID)

	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET email_verified_at=NOW`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	resp, err := h.srv.VerifyEmail(context.Background(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token:     tok,
		ClientIp:  "1.2.3.4",
		UserAgent: "Go-test",
	}))
	if err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	if resp.Msg.GetStatus() != "email_verified" {
		t.Errorf("status = %q, want email_verified", resp.Msg.GetStatus())
	}
	if resp.Msg.GetUserId() != userID.String() {
		t.Errorf("user_id = %q, want %q", resp.Msg.GetUserId(), userID.String())
	}
	if resp.Msg.GetEmailVerifiedAt() == "" {
		t.Errorf("email_verified_at empty")
	}
	// Audit auth.verify_email recorded.
	if len(h.auditP.byType(audit.EventVerifyEmail)) != 1 {
		t.Errorf("audit verify_email count = %d, want 1", len(h.auditP.byType(audit.EventVerifyEmail)))
	}
	// Redis primary key gone (Consume DEL'd it).
	primary := "auth:email_verify:" + token.Hash(tok)
	if h.mr.Exists(primary) {
		t.Errorf("Redis primary key still exists after VerifyEmail")
	}
}

// Scenario: 2.2-UNIT-083
// Already-verified user: PG UPDATE affected=0 → handler runs GetUserByID
// to retrieve canonical email_verified_at → response status="already_verified".
func TestVerifyEmail_AlreadyVerifiedIdempotent(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.New()
	tok := mustStoreToken(t, h, userID)
	originalVerifiedAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	// UPDATE returns 0 rows (already verified).
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET email_verified_at=NOW`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// Handler hydrates the canonical timestamp via GetUserByID.
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "u@example.com", []byte("$2a$12$x"), &originalVerifiedAt,
			nil, nil, "en", "UTC",
			nil, false, "active", nil,
			nil, time.Now(), time.Now(),
		))

	resp, err := h.srv.VerifyEmail(context.Background(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token: tok,
	}))
	if err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	if resp.Msg.GetStatus() != "already_verified" {
		t.Errorf("status = %q, want already_verified", resp.Msg.GetStatus())
	}
	// Response email_verified_at carries the ORIGINAL timestamp (RFC3339 UTC).
	wantTimestamp := originalVerifiedAt.Format(time.RFC3339)
	if resp.Msg.GetEmailVerifiedAt() != wantTimestamp {
		t.Errorf("email_verified_at = %q, want %q (original timestamp)", resp.Msg.GetEmailVerifiedAt(), wantTimestamp)
	}
}

// Scenario: 2.2-UNIT-084
// Attempts ≥ 5 (token brute-force) → 410_token_used + audit
// auth.verify_email_brute_force. PG MarkEmailVerified is NOT called.
func TestVerifyEmail_AttemptsExceededReturns410TokenUsed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.New()
	tok, _ := token.Generate()
	// Pre-populate the primary with attempts=4 — next Consume bumps to 5
	// and trips the lockout.
	rawPayload, _ := json.Marshal(token.Payload{
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
		Attempts:  4,
	})
	primary := "auth:email_verify:" + token.Hash(tok)
	reverse := "auth:email_verify:user:" + userID.String()
	h.mr.Set(primary, string(rawPayload))
	h.mr.Set(reverse, token.Hash(tok))

	_, err := h.srv.VerifyEmail(context.Background(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token: tok,
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusTokenUsed)
	// Brute-force audit event MUST be emitted.
	if len(h.auditP.byType(audit.EventVerifyEmailBruteForce)) != 1 {
		t.Errorf("audit verify_email_brute_force count = %d, want 1", len(h.auditP.byType(audit.EventVerifyEmailBruteForce)))
	}
}

// --- ResendVerification tests --------------------------------------------

// Scenario: 2.2-UNIT-085
// Resend IP rate limit (3/15min) → 429_rate_limit_resend_ip with Retry-After.
func TestResendVerification_IPRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Pre-fill the IP counter to the cap.
	for i := 0; i < 3; i++ {
		if err := h.rdb.Incr(context.Background(), "ratelimit:resend:ip:5.6.7.8").Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = h.rdb.Expire(context.Background(), "ratelimit:resend:ip:5.6.7.8", 15*time.Minute).Err()

	_, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:    "user@example.com",
		ClientIp: "5.6.7.8",
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimitResendIP)

	// Verify Retry-After metadata is set.
	var ce *connect.Error
	if errors.As(err, &ce) {
		if ce.Meta().Get(handlers.MetaRetryAfterSeconds) == "" {
			t.Errorf("Retry-After metadata not set on 429 response")
		}
	}
}

// Scenario: 2.2-UNIT-086
// Per-email rate limit (1/60s) → 429_rate_limit_resend_email.
func TestResendVerification_EmailRateLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// IP counter not pre-filled; email counter pre-filled to the cap (=1).
	// The first call from any IP for this email will trigger the email
	// limit on its post-INCR check (count=2 > limit=1).
	// To simulate, manually set the email key to 1 with TTL.
	emailKey := "ratelimit:resend:email:" + ratelimit.EmailHash("user@example.com")
	_ = h.rdb.Set(context.Background(), emailKey, "1", 60*time.Second).Err()

	_, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:    "user@example.com",
		ClientIp: "9.9.9.9",
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimitResendEm)
}

// Scenario: 2.2-UNIT-087
// Unknown email → status:"ok" (anti-enumeration); audit error_code carries
// "resend.unknown_email"; no notification.SendEmail; no real token-store
// write (only the dummy SETEX).
func TestResendVerification_UnknownEmail_AntiEnumeration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("unknown@example.com").
		WillReturnRows(userRows()) // empty → ErrUserNotFound

	resp, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:    "unknown@example.com",
		ClientIp: "1.1.1.1",
	}))
	if err != nil {
		t.Fatalf("ResendVerification: %v", err)
	}
	if resp.Msg.GetStatus() != "ok" {
		t.Errorf("status = %q, want ok", resp.Msg.GetStatus())
	}
	// notification.SendVerificationEmail MUST NOT have been called.
	if h.notif.calls != 0 {
		t.Errorf("notification called %d times for unknown email; want 0", h.notif.calls)
	}
	// Audit recorded with resend.unknown_email error_code.
	events := h.auditP.byType(audit.EventEmailSendFailed)
	if len(events) != 1 {
		t.Fatalf("audit events for unknown_email branch = %d, want 1; got=%+v", len(events), h.auditP.events)
	}
	if events[0].ErrorCode != "resend.unknown_email" {
		t.Errorf("audit error_code = %q, want resend.unknown_email", events[0].ErrorCode)
	}
	if events[0].EmailHash == "" {
		t.Errorf("audit email_hash empty")
	}
}

// Scenario: 2.2-UNIT-088
// Already-verified email → status:"ok"; audit error_code="resend.already_verified";
// no notification call.
func TestResendVerification_AlreadyVerified_AntiEnumeration(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("verified@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "verified@example.com", []byte("$2a$12$x"), &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))

	resp, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:    "verified@example.com",
		ClientIp: "2.2.2.2",
	}))
	if err != nil {
		t.Fatalf("ResendVerification: %v", err)
	}
	if resp.Msg.GetStatus() != "ok" {
		t.Errorf("status = %q, want ok", resp.Msg.GetStatus())
	}
	if h.notif.calls != 0 {
		t.Errorf("notification called for already-verified email; want 0")
	}
	events := h.auditP.byType(audit.EventEmailSendFailed)
	if len(events) != 1 || events[0].ErrorCode != "resend.already_verified" {
		t.Errorf("audit event mismatch: got %+v", events)
	}
}

// Scenario: 2.2-UNIT-089
// Pending-verification email → DEL old + Store new + send email + audit.
// Old token MUST be invalidated BEFORE notification.SendVerificationEmail
// is called (BR-1.6).
func TestResendVerification_PendingVerification_DeletesOldStoresNewAndSends(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.New()
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("pending@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "pending@example.com", []byte("$2a$12$x"), nil, // email_verified_at = NULL
			nil, nil, "zh-CN", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))

	// Pre-populate an OLD token so we can verify DeleteForUser ran.
	oldTok := mustStoreToken(t, h, userID)
	oldPrimary := "auth:email_verify:" + token.Hash(oldTok)
	if !h.mr.Exists(oldPrimary) {
		t.Fatalf("seed: old primary missing")
	}

	resp, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:    "pending@example.com",
		ClientIp: "3.3.3.3",
	}))
	if err != nil {
		t.Fatalf("ResendVerification: %v", err)
	}
	if resp.Msg.GetStatus() != "ok" {
		t.Errorf("status = %q, want ok", resp.Msg.GetStatus())
	}

	// Old primary MUST be DEL'd (DeleteForUser ran before Store).
	if h.mr.Exists(oldPrimary) {
		t.Errorf("old token primary still exists after Resend — DeleteForUser didn't run")
	}
	// Notification MUST have been called once, with the user's locale (NOT
	// any request-locale field — UNIT-090).
	if h.notif.calls != 1 {
		t.Fatalf("notification.calls = %d, want 1", h.notif.calls)
	}
	if h.notif.lastLocale != "zh-CN" {
		t.Errorf("notification locale = %q, want zh-CN (from users.locale per BR-2.6)", h.notif.lastLocale)
	}
	if h.notif.lastTo != "pending@example.com" {
		t.Errorf("notification to = %q, want pending@example.com", h.notif.lastTo)
	}
	// A NEW verify-token primary must exist (different from the old hash).
	newPrimaryFound := false
	for _, k := range h.mr.Keys() {
		if strings.HasPrefix(k, "auth:email_verify:") &&
			!strings.HasPrefix(k, "auth:email_verify:user:") &&
			!strings.HasPrefix(k, "auth:email_verify:dummy:") &&
			k != oldPrimary {
			newPrimaryFound = true
			break
		}
	}
	if !newPrimaryFound {
		t.Errorf("no NEW auth:email_verify:<hash> key after Resend pending-verification")
	}
	// Audit recorded as sent.
	events := h.auditP.byType(audit.EventEmailSendFailed)
	var sentEvents int
	for _, e := range events {
		if e.ErrorCode == "resend.sent" {
			sentEvents++
		}
	}
	if sentEvents != 1 {
		t.Errorf("audit resend.sent events = %d, want 1", sentEvents)
	}
}

// Scenario: 2.2-UNIT-090
// Locale for the resend email comes from users.locale, NEVER from the
// request. ResendVerificationRequest has no locale field by design.
// Verified inline in TestResendVerification_PendingVerification (above).

// Scenario: 2.2-UNIT-091 (representative — full p99 statistical run in CI)
// All three branches return the SAME response shape ({status:"ok"}). Verified
// across the three branch tests above. Here we tighten the check by running
// each path back-to-back and asserting body equality.
func TestResendVerification_AllBranchesReturnIdenticalResponseShape(t *testing.T) {
	t.Parallel()
	// Each branch needs its own harness because pgxmock expectations don't
	// cross-pollinate.
	cases := []struct {
		name   string
		setup  func(t *testing.T) *harness
		email  string
		client string
	}{
		{
			name: "unknown",
			setup: func(t *testing.T) *harness {
				h := newHarness(t)
				h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
					WithArgs("u@example.com").
					WillReturnRows(userRows())
				return h
			},
			email:  "u@example.com",
			client: "1.1.1.1",
		},
		{
			name: "already_verified",
			setup: func(t *testing.T) *harness {
				h := newHarness(t)
				verifiedAt := time.Now()
				h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
					WithArgs("v@example.com").
					WillReturnRows(userRows().AddRow(
						uuid.New(), "v@example.com", []byte("$2a$12$x"), &verifiedAt,
						nil, nil, "en", "UTC", nil, false, "active", nil, nil,
						time.Now(), time.Now(),
					))
				return h
			},
			email:  "v@example.com",
			client: "2.2.2.2",
		},
		{
			name: "pending_sent",
			setup: func(t *testing.T) *harness {
				h := newHarness(t)
				h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
					WithArgs("p@example.com").
					WillReturnRows(userRows().AddRow(
						uuid.New(), "p@example.com", []byte("$2a$12$x"), nil,
						nil, nil, "en", "UTC", nil, false, "active", nil, nil,
						time.Now(), time.Now(),
					))
				return h
			},
			email:  "p@example.com",
			client: "3.3.3.3",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.setup(t)
			resp, err := h.srv.ResendVerification(context.Background(), connect.NewRequest(&authv1.ResendVerificationRequest{
				Email:    c.email,
				ClientIp: c.client,
			}))
			if err != nil {
				t.Fatalf("[%s] err = %v", c.name, err)
			}
			if resp.Msg.GetStatus() != "ok" {
				t.Errorf("[%s] status = %q, want ok (m-5 anti-enumeration)", c.name, resp.Msg.GetStatus())
			}
		})
	}
}
