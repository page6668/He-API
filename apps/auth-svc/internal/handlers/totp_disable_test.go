package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
)

// Scenario: 2.4-UNIT-080 — DisableTOTP happy path (factor=TOTP): clears
// users.totp_*, deletes all recovery codes, HIGH audit + Alert2FADisabled email.
func TestDisableTOTP_TOTPFactorHappyPath(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	secret := []byte("12345678901234567890")
	enc := base64Encode(secret)
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")

	// GetUserByID + GetTOTPSecret (factor verify path).
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true, "active", nil, nil,
			time.Now(), time.Now(),
		))
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	// Single PG transaction wraps the two clean-slate statements (AC4 / BR-3.9).
	h.mock.ExpectBegin()
	// ClearTOTPSecret.
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET totp_secret_encrypted=NULL, totp_enabled=FALSE`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// DeleteAllRecoveryCodesForUser.
	h.mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes WHERE user_id=\$1`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("DELETE", 10))
	h.mock.ExpectCommit()

	code := totpGenerate(secret, fixedNow)
	resp, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     code,
		ClientIp:  "1.2.3.4",
		UserAgent: "Mozilla/5.0",
	}))
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if !resp.Msg.GetOk() {
		t.Errorf("ok=false")
	}
	// Audit HIGH.
	events := h.auditP.byType(audit.Event2FADisabled)
	if len(events) != 1 {
		t.Fatalf("want 1 disabled audit, got %d", len(events))
	}
	if sev, _ := events[0].Metadata["severity"].(string); sev != audit.SeverityHigh {
		t.Errorf("severity = %q, want HIGH", sev)
	}
	if dm, _ := events[0].Metadata["disable_method"].(string); dm != "totp" {
		t.Errorf("disable_method = %q, want totp", dm)
	}
	// Email sent with HIGH-severity template.
	if h.notif.alertCalls != 1 {
		t.Errorf("alert calls = %d, want 1", h.notif.alertCalls)
	}
	if h.notif.lastAlert != notification.Alert2FADisabled {
		t.Errorf("alert template = %v, want Alert2FADisabled", h.notif.lastAlert)
	}
}

// Scenario: 2.4-UNIT-082 — DisableTOTP factor=PASSWORD happy path.
func TestDisableTOTP_PasswordFactorHappyPath(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	pwHash := mustHash(t, "correct horse battery staple")
	verifiedAt := time.Now().Add(-time.Hour)
	enc := base64Encode([]byte("12345678901234567890"))

	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true, "active", nil, nil,
			time.Now(), time.Now(),
		))
	h.mock.ExpectBegin()
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET totp_secret_encrypted=NULL, totp_enabled=FALSE`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	h.mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes WHERE user_id=\$1`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("DELETE", 10))
	h.mock.ExpectCommit()

	resp, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD,
		Value:     "correct horse battery staple",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	if err != nil {
		t.Fatalf("Disable password: %v", err)
	}
	if !resp.Msg.GetOk() {
		t.Errorf("ok=false")
	}
	events := h.auditP.byType(audit.Event2FADisabled)
	if got := len(events); got != 1 {
		t.Fatalf("want 1 audit, got %d", got)
	}
	if dm, _ := events[0].Metadata["disable_method"].(string); dm != "password" {
		t.Errorf("disable_method = %q, want password", dm)
	}
}

// Scenario: 2.4-UNIT-083 — wrong TOTP factor → 401_invalid_credentials,
// counter increments (no PG mutation).
func TestDisableTOTP_WrongCodeIncrementsCounter(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	enc := base64Encode([]byte("12345678901234567890"))
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")

	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true, "active", nil, nil,
			time.Now(), time.Now(),
		))
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	_, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     "000000",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
	// No alert email (only fires on success).
	if h.notif.alertCalls != 0 {
		t.Errorf("alert should NOT fire on wrong factor")
	}
}

// Scenario: 2.4-UNIT-084 — not enrolled (totp_enabled=FALSE) → 409_not_enrolled.
func TestDisableTOTP_NotEnrolled(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD,
		Value:     "x",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusNotEnrolled)
}

// Scenario: 2.4-BLIND-DATA-002 (QA Round 1 H1/H2 fix) — Disable transaction
// atomicity. Inject a connection failure on the second statement (DELETE
// recovery codes) and assert:
//
//   - the overall handler returns 500 (internal error);
//   - pgxmock observes ExpectBegin + ExpectRollback (no Commit);
//   - no Alert2FADisabled email is sent (success path never reached);
//   - no `2fa.disabled` audit is emitted.
//
// Without the transaction this scenario would leave the user with
// totp_enabled=FALSE in users + intact (orphan) recovery_code rows —
// the AC4 / BR-3.9 "single PG transaction" violation flagged by QA-2.4-H1.
func TestDisableTOTP_TransactionRollsBackOnRecoveryDeleteFailure(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	secret := []byte("12345678901234567890")
	enc := base64Encode(secret)
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")

	// GetUserByID + GetTOTPSecret (factor verify path).
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true, "active", nil, nil,
			time.Now(), time.Now(),
		))
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	// Begin → first statement OK → second statement FAILS → Rollback.
	h.mock.ExpectBegin()
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET totp_secret_encrypted=NULL, totp_enabled=FALSE`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	h.mock.ExpectExec(`DELETE FROM he_api\.mfa_recovery_codes WHERE user_id=\$1`).
		WithArgs(userID).
		WillReturnError(errors.New("conn dead mid-tx"))
	h.mock.ExpectRollback()

	code := totpGenerate(secret, fixedNow)
	_, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     code,
		ClientIp:  "1.2.3.4",
		UserAgent: "Mozilla/5.0",
	}))
	if err == nil {
		t.Fatalf("want internal error on mid-tx failure, got nil")
	}
	assertConnectStatus(t, err, connect.CodeInternal, handlers.StatusAuthSvcUnavailable)

	// No success-path side effects: no alert email, no `2fa.disabled` audit.
	if h.notif.alertCalls != 0 {
		t.Errorf("alert email fired despite tx rollback: %d calls", h.notif.alertCalls)
	}
	if events := h.auditP.byType(audit.Event2FADisabled); len(events) != 0 {
		t.Errorf("2fa.disabled audit emitted despite tx rollback: %d events", len(events))
	}
	// pgxmock ExpectationsWereMet (in harness Cleanup) enforces that
	// Begin/Rollback fired and Commit did NOT.
}

// Scenario: 2.4-UNIT-085 — invalid factor enum → 400_invalid_factor.
func TestDisableTOTP_InvalidFactor(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	_, err := h.srv.DisableTOTP(context.Background(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_UNSPECIFIED,
		Value:     "x",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidFactor)
}
