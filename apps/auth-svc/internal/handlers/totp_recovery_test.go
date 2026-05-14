package handlers_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/recovery"
	totppkg "github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

func totpGenerate(secret []byte, t time.Time) string {
	return totppkg.Generate(secret, t)
}

// Scenario: 2.4-UNIT-063 — UseRecoveryCode happy path: hyphen-stripped input
// matches an unused row, row marked used atomically, access+refresh issued
// with aal=2, remaining count + low_codes signal returned, HIGH audit +
// SendSecurityAlert(Alert2FARecoveryUsed).
func TestUseRecoveryCode_HappyPath(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	ip, ua := "1.2.3.4", "Mozilla/5.0"

	// Generate a code; hash it; mock will return ID + hash on ListUnused.
	code, _ := recovery.GenerateCode()
	hash, _ := recovery.Hash(code)
	matchedID := uuid.New()
	otherID := uuid.New()
	otherHash, _ := recovery.Hash("OTHERCODE2")

	tok := stageMFAToken(t, h, userID, ip, ua, "/dashboard")

	// Expectations: ListUnusedRecoveryCodes (returns 2 rows; ours + a decoy).
	h.mock.ExpectQuery(`SELECT id, code_hash FROM he_api\.mfa_recovery_codes`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "code_hash"}).
			AddRow(matchedID, hash).
			AddRow(otherID, otherHash))
	// MarkRecoveryCodeUsed (atomic UPDATE WHERE used_at IS NULL).
	h.mock.ExpectExec(`UPDATE he_api\.mfa_recovery_codes\s+SET used_at=NOW`).
		WithArgs(matchedID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// CountUnusedRecoveryCodes.
	h.mock.ExpectQuery(`SELECT COUNT\(\*\) FROM he_api\.mfa_recovery_codes`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2)) // 2 remaining → low
	// MarkTOTPUsed.
	h.mock.ExpectExec(`UPDATE he_api\.users SET totp_last_used_at=NOW`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	resp, err := h.srv.UseRecoveryCode(context.Background(), connect.NewRequest(&authv1.UseRecoveryCodeRequest{
		MfaToken:  tok,
		Code:      recovery.Display(code), // submit with hyphens
		ClientIp:  ip,
		UserAgent: ua,
	}))
	if err != nil {
		t.Fatalf("UseRecoveryCode: %v", err)
	}
	if resp.Msg.GetAal() != 2 {
		t.Errorf("aal = %d, want 2", resp.Msg.GetAal())
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Errorf("access_token empty")
	}
	if resp.Msg.GetRecoveryCodesRemaining() != 2 {
		t.Errorf("remaining = %d, want 2", resp.Msg.GetRecoveryCodesRemaining())
	}
	if !resp.Msg.GetRecoveryCodesLow() {
		t.Errorf("low flag should be true at remaining=2")
	}
	if resp.Msg.GetReturnTo() != "/dashboard" {
		t.Errorf("return_to = %q", resp.Msg.GetReturnTo())
	}

	// Audit HIGH severity.
	events := h.auditP.byType(audit.Event2FARecoveryUsed)
	if len(events) != 1 {
		t.Fatalf("want 1 recovery_used audit, got %d", len(events))
	}
	// Severity HIGH per BR-5.7.
	if sev, _ := events[0].Metadata["severity"].(string); sev != audit.SeverityHigh {
		t.Errorf("severity = %q, want HIGH", sev)
	}

	// Email sent.
	if h.notif.alertCalls != 1 {
		t.Errorf("SendSecurityAlert calls = %d, want 1", h.notif.alertCalls)
	}
	if h.notif.lastAlert != 2 { // notification.Alert2FARecoveryUsed = iota+1 starting at 1 → 2
		t.Errorf("alert template = %v, want Alert2FARecoveryUsed", h.notif.lastAlert)
	}

	// JTI consumed.
	for _, k := range h.mr.Keys() {
		if startsWith(k, "auth:2fa:challenge:") {
			t.Errorf("JTI still present after recovery success: %s", k)
		}
	}
}

// Scenario: 2.4-UNIT-065 — wrong code: no unused row matches → 401_invalid_recovery_code,
// counter increments, JTI remains.
func TestUseRecoveryCode_WrongCode(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("99999999-9999-9999-9999-999999999999")
	ip, ua := "1.2.3.4", "ua"

	// 2 unused codes, neither matches the submitted "AAAAAAAAA2".
	h1, _ := recovery.Hash("WRONGCODE2")
	h2, _ := recovery.Hash("OTHERCODE2")
	h.mock.ExpectQuery(`SELECT id, code_hash FROM he_api\.mfa_recovery_codes`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "code_hash"}).
			AddRow(uuid.New(), h1).
			AddRow(uuid.New(), h2))

	tok := stageMFAToken(t, h, userID, ip, ua, "")
	_, err := h.srv.UseRecoveryCode(context.Background(), connect.NewRequest(&authv1.UseRecoveryCodeRequest{
		MfaToken:  tok,
		Code:      "AAAAAAAAA2",
		ClientIp:  ip,
		UserAgent: ua,
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidRecoveryCode)

	// JTI still in redis.
	hasJTI := false
	for _, k := range h.mr.Keys() {
		if startsWith(k, "auth:2fa:challenge:") {
			hasJTI = true
		}
	}
	if !hasJTI {
		t.Errorf("JTI should remain on wrong recovery code")
	}
}

// Scenario: 2.4-UNIT-067 — 0 unused codes → 410_no_recovery_codes (distinct
// from invalid-code so UI can prompt regenerate).
func TestUseRecoveryCode_NoRecoveryCodesLeft(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	ip, ua := "1.2.3.4", "ua"

	h.mock.ExpectQuery(`SELECT id, code_hash FROM he_api\.mfa_recovery_codes`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{"id", "code_hash"}))

	tok := stageMFAToken(t, h, userID, ip, ua, "")
	_, err := h.srv.UseRecoveryCode(context.Background(), connect.NewRequest(&authv1.UseRecoveryCodeRequest{
		MfaToken:  tok,
		Code:      "ANYCODE234",
		ClientIp:  ip,
		UserAgent: ua,
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusNoRecoveryCodes)
}

// Scenario: 2.4-UNIT-064 — code format invalid (post-normalize fails) → 400.
func TestUseRecoveryCode_InvalidFormat(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	tok := stageMFAToken(t, h, userID, "1.2.3.4", "ua", "")
	_, err := h.srv.UseRecoveryCode(context.Background(), connect.NewRequest(&authv1.UseRecoveryCodeRequest{
		MfaToken:  tok,
		Code:      "BAD!", // non-alphanumeric → after strip → too short
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidRecoveryCodeFormat)
}

// Scenario: 2.4-UNIT-073 — RegenerateRecoveryCodes happy path (factor=TOTP):
// KMS-decrypt secret + TOTP validate → mark-all-unused-as-used + insert 10 new,
// MEDIUM audit + email + plaintext-returned-once.
func TestRegenerateRecoveryCodes_TOTPFactorHappyPath(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	// Stage user with totp_enabled + a secret (NoOp KMS — store raw bytes via
	// the repository's base64 wrapper).
	secret := []byte("12345678901234567890") // RFC 6238 §5 test secret
	enc := base64Encode(secret)
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true /* totp_enabled */, "active", nil, nil,
			time.Now(), time.Now(),
		))
	// verifyRegenerateFactor reads totp_secret_encrypted via GetTOTPSecret.
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))
	// Mark all unused old codes.
	h.mock.ExpectExec(`UPDATE he_api\.mfa_recovery_codes\s+SET used_at=NOW.*regenerated_at`).
		WithArgs(userID, "user_initiated", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 5))
	// Bulk insert 10 new codes.
	for i := 0; i < 10; i++ {
		h.mock.ExpectExec(`INSERT INTO he_api\.mfa_recovery_codes`).
			WithArgs(userID, pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}

	// Generate a valid TOTP code at fixedNow against the RFC test secret.
	code := generateTOTPAtFixedNow(t, secret)

	resp, err := h.srv.RegenerateRecoveryCodes(context.Background(), connect.NewRequest(&authv1.RegenerateRecoveryCodesRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     code,
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if len(resp.Msg.GetRecoveryCodes()) != 10 {
		t.Errorf("recovery_codes len=%d want 10", len(resp.Msg.GetRecoveryCodes()))
	}
	if h.notif.alertCalls != 1 {
		t.Errorf("alert calls = %d want 1", h.notif.alertCalls)
	}
	if h.notif.lastAlert != 3 { // notification.Alert2FARecoveryRegenerated
		t.Errorf("alert template = %v want Alert2FARecoveryRegenerated", h.notif.lastAlert)
	}
	events := h.auditP.byType(audit.Event2FARecoveryRegenerated)
	if len(events) != 1 {
		t.Fatalf("want 1 regenerated audit, got %d", len(events))
	}
}

// Scenario: 2.4-UNIT-075 — wrong TOTP code → 401_invalid_credentials, no PG mutation.
func TestRegenerateRecoveryCodes_WrongTOTPRejected(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	secret := []byte("12345678901234567890")
	enc := base64Encode(secret)
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

	_, err := h.srv.RegenerateRecoveryCodes(context.Background(), connect.NewRequest(&authv1.RegenerateRecoveryCodesRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     "000000",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
	if h.notif.alertCalls != 0 {
		t.Errorf("alert should NOT fire on wrong factor")
	}
}

// Scenario: 2.4-UNIT-076 — not-enrolled (totp_enabled=FALSE) → 409.
func TestRegenerateRecoveryCodes_NotEnrolled(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "irrelevant")
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", nil, false /* totp_enabled */, "active", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.RegenerateRecoveryCodes(context.Background(), connect.NewRequest(&authv1.RegenerateRecoveryCodesRequest{
		UserId:    userID.String(),
		Factor:    authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		Value:     "123456",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusNotEnrolled)
}

// generateTOTPAtFixedNow imports the totp package to produce a code that
// validates at handler-side fixedNow.
func generateTOTPAtFixedNow(t *testing.T, secret []byte) string {
	t.Helper()
	// Reach across to the totp package.
	return totpGenerate(secret, fixedNow)
}
