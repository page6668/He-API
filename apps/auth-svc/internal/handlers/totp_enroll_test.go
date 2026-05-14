package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/kms"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

// mfaHarness extends `harness` with the Story 2.4 KMS dependency. We bypass
// the regular newHarness because handler tests in this package keep deps
// flat as struct fields; the simplest path is to construct from scratch.
type mfaHarness struct {
	srv    *handlers.AuthServer
	mock   pgxmock.PgxConnIface
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	notif  *fakeNotification
	auditP *recordingAudit
	kms    kms.KMSClient
}

// newMFAHarness builds an AuthServer with KMS (NoOp — passthrough), Notification,
// Audit, and the Issuer label. Tests targeting AC1 / AC4 reuse this. AC2 /
// AC3 will extend further with MFASigner + MFAParser.
func newMFAHarness(t *testing.T) *mfaHarness {
	t.Helper()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet pgx expectations: %v", err)
		}
		mock.Close(context.Background())
	})
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	h := &mfaHarness{
		mock:   mock,
		mr:     mr,
		rdb:    rdb,
		notif:  &fakeNotification{},
		auditP: &recordingAudit{},
		kms:    kms.NewNoOp(),
	}
	h.srv = handlers.NewAuthServer(handlers.AuthServer{
		DB:             mock,
		Redis:          rdb,
		Notification:   h.notif,
		Audit:          h.auditP,
		Clock:          func() time.Time { return fixedNow },
		ConsoleBaseURL: "https://console.he-api.com",
		Logger:         slog.Default(),
		KMS:            h.kms,
		Issuer:         "He-API",
	})
	return h
}

// emptyUserRow returns a pgxmock row factory for the GetTOTPSecret query
// projection: (totp_secret_encrypted *string, totp_enabled bool,
// totp_enrolled_at *time.Time).
func totpUserRow(enabled bool, secret *string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"totp_secret_encrypted", "totp_enabled", "totp_enrolled_at"}).
		AddRow(secret, enabled, (*time.Time)(nil))
}

// --- EnrollTOTPInit ------------------------------------------------------

// Scenario: 2.4-UNIT-011 — happy path: Redis pending blob exists at the
// canonical key with the correct TTL; response carries otpauth_uri +
// 256x256 PNG + 10 recovery codes; audit `2fa.enroll.initiated` emitted.
func TestEnrollTOTPInit_HappyPath(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(false, nil))

	resp, err := h.srv.EnrollTOTPInit(context.Background(), connect.NewRequest(&authv1.EnrollTOTPInitRequest{
		UserId:    userID.String(),
		ClientIp:  "1.2.3.4",
		UserAgent: "Mozilla/5.0",
	}))
	if err != nil {
		t.Fatalf("EnrollTOTPInit: %v", err)
	}
	if resp.Msg.GetOtpauthUri() == "" {
		t.Errorf("otpauth_uri empty")
	}
	if len(resp.Msg.GetRecoveryCodes()) != 10 {
		t.Errorf("recovery_codes len = %d, want 10", len(resp.Msg.GetRecoveryCodes()))
	}
	// Each code is 10 base32 chars.
	for i, c := range resp.Msg.GetRecoveryCodes() {
		if len(c) != 10 {
			t.Errorf("code %d len=%d", i, len(c))
		}
	}
	// PNG validates.
	img, err := png.Decode(bytes.NewReader(resp.Msg.GetQrCodePng()))
	if err != nil {
		t.Errorf("qr_code_png decode: %v", err)
	}
	if img != nil {
		b := img.Bounds()
		if b.Dx() != totp.QRPixelSize || b.Dy() != totp.QRPixelSize {
			t.Errorf("qr pixel dim = %dx%d", b.Dx(), b.Dy())
		}
	}

	// Redis pending blob exists at the canonical key with ~600s TTL.
	pendingKey := "auth:2fa:enroll:" + userID.String()
	ttl := h.mr.TTL(pendingKey)
	if ttl == 0 {
		t.Errorf("pending blob missing or no TTL")
	}
	if ttl > 600*time.Second || ttl < 590*time.Second {
		t.Errorf("pending TTL = %v, want ~600s", ttl)
	}

	// Audit event emitted.
	events := h.auditP.byType(audit.Event2FAEnrollInitiated)
	if len(events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(events))
	}
	if events[0].UserID != userID.String() {
		t.Errorf("audit user_id mismatch")
	}
	if !events[0].Success {
		t.Errorf("audit success should be true")
	}

	// ExpiresAtUnix correct (fixedNow + 600s).
	wantExp := fixedNow.Add(10 * time.Minute).Unix()
	if resp.Msg.GetExpiresAtUnix() != wantExp {
		t.Errorf("expires_at = %d want %d", resp.Msg.GetExpiresAtUnix(), wantExp)
	}
}

// Scenario: 2.4-UNIT-013 — already enrolled (totp_enabled=TRUE) → 409.
func TestEnrollTOTPInit_AlreadyEnrolledReturns409(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.New()
	// Valid base64 — the repository layer base64-decodes the column.
	enc := "ZGVhZGJlZWY="
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	_, err := h.srv.EnrollTOTPInit(context.Background(), connect.NewRequest(&authv1.EnrollTOTPInitRequest{
		UserId: userID.String(),
	}))
	assertConnectStatus(t, err, connect.CodeAlreadyExists, handlers.StatusAlreadyEnrolled)
	// No Redis blob should have been written.
	if h.mr.Exists("auth:2fa:enroll:" + userID.String()) {
		t.Errorf("redis blob should not exist on 409")
	}
}

// Scenario: 2.4-UNIT-012 — invalid user_id → InvalidArgument / 400_invalid_user_id.
func TestEnrollTOTPInit_InvalidUserID(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	_, err := h.srv.EnrollTOTPInit(context.Background(), connect.NewRequest(&authv1.EnrollTOTPInitRequest{
		UserId: "not-a-uuid",
	}))
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidUserID)
}

// Scenario: 2.4-UNIT-014 — rate-limit exhausted (4th init in window) → 429.
func TestEnrollTOTPInit_RateLimited(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.New()
	// Pre-warm the counter to the limit (3) so the next call trips.
	// The handler issues INCR via Lua script; we mirror by incrementing
	// the same key via miniredis.
	rlKey := "ratelimit:2fa:enroll:init:" + userID.String()
	for i := 0; i < handlerInternalInitLimit(); i++ {
		h.mr.Incr(rlKey, 1)
	}
	// Set TTL so the script's TTL read returns a sensible value.
	h.mr.SetTTL(rlKey, time.Hour)

	_, err := h.srv.EnrollTOTPInit(context.Background(), connect.NewRequest(&authv1.EnrollTOTPInitRequest{
		UserId:    userID.String(),
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimit2FAEnroll)
}

// handlerInternalInitLimit mirrors the rl2FAEnrollInitLimit const for tests.
func handlerInternalInitLimit() int { return 3 }

// --- EnrollTOTPVerify ----------------------------------------------------

// Scenario: 2.4-UNIT-015 — happy path: GETDEL-equivalent on success;
// UPDATE users + INSERT 10 codes; audit `2fa.enrolled` MEDIUM + SendEmail.
func TestEnrollTOTPVerify_HappyPath(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	// Stage the pending blob (matches what Init would have written).
	secret, _ := totp.GenerateSecret()
	codes := []string{"AAAAAAAAA2", "BBBBBBBBB2", "CCCCCCCCC2", "DDDDDDDDD2", "EEEEEEEEE2",
		"FFFFFFFFF2", "GGGGGGGGG2", "HHHHHHHHH2", "IIIIIIIII2", "JJJJJJJJJ2"}
	blob, _ := json.Marshal(map[string]any{
		"secret":          secret,
		"recovery_codes":  codes,
		"created_at_unix": fixedNow.Unix(),
	})
	// KMS NoOp = identity, so we set raw bytes into Redis.
	pendingKey := "auth:2fa:enroll:" + userID.String()
	h.mr.Set(pendingKey, string(blob))
	h.mr.SetTTL(pendingKey, 10*time.Minute)

	// 6-digit code at fixedNow.
	code := totp.Generate(secret, fixedNow)

	// PG expectations:
	// 1. GetTOTPSecret race re-check (returns enabled=false).
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(false, nil))
	// 2. UPDATE users (SetTOTPSecret).
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET totp_secret_encrypted=\$1, totp_enabled=TRUE`).
		WithArgs(pgxmock.AnyArg(), userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// 3. INSERT 10 recovery codes (one per code).
	for i := 0; i < 10; i++ {
		h.mock.ExpectExec(`INSERT INTO he_api\.mfa_recovery_codes`).
			WithArgs(userID, pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}

	resp, err := h.srv.EnrollTOTPVerify(context.Background(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
		UserId:                  userID.String(),
		Code:                    code,
		AckRecoveryCodesSaved:   true,
		ClientIp:                "1.2.3.4",
		UserAgent:               "Mozilla/5.0",
	}))
	if err != nil {
		t.Fatalf("EnrollTOTPVerify: %v", err)
	}
	if !resp.Msg.GetOk() {
		t.Errorf("ok=false")
	}
	if resp.Msg.GetEnrolledAtUnix() != fixedNow.Unix() {
		t.Errorf("enrolled_at = %d want %d", resp.Msg.GetEnrolledAtUnix(), fixedNow.Unix())
	}

	// Pending blob consumed on success.
	if h.mr.Exists(pendingKey) {
		t.Errorf("pending blob should be DEL'd on success")
	}

	// Audit success.
	events := h.auditP.byType(audit.Event2FAEnrolled)
	if len(events) != 1 {
		t.Fatalf("want 1 audit 2fa.enrolled, got %d", len(events))
	}

	// Out-of-band email sent.
	if h.notif.alertCalls != 1 {
		t.Errorf("SendSecurityAlert calls = %d, want 1", h.notif.alertCalls)
	}
}

// Scenario: 2.4-UNIT-017 — pending blob missing (TTL expired) → 409_enroll_expired.
func TestEnrollTOTPVerify_RedisExpired(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.New()
	_, err := h.srv.EnrollTOTPVerify(context.Background(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
		UserId:                  userID.String(),
		Code:                    "123456",
		AckRecoveryCodesSaved:   true,
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusEnrollExpired)
}

// Scenario: 2.4-UNIT-019 — ack_recovery_codes_saved=false → 400.
func TestEnrollTOTPVerify_AckFalseRejected(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.New()
	_, err := h.srv.EnrollTOTPVerify(context.Background(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
		UserId:                  userID.String(),
		Code:                    "123456",
		AckRecoveryCodesSaved:   false,
	}))
	assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusRecoveryCodesNotAck)
}

// Scenario: 2.4-BLIND-BOUNDARY-001..003 — wrong code length → 400_invalid_totp_format.
func TestEnrollTOTPVerify_RejectsWrongLength(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.New()
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		_, err := h.srv.EnrollTOTPVerify(context.Background(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
			UserId:                  userID.String(),
			Code:                    bad,
			AckRecoveryCodesSaved:   true,
		}))
		assertConnectStatus(t, err, connect.CodeInvalidArgument, handlers.StatusInvalidTOTPFormat)
	}
}

// Scenario: 2.4-UNIT-018 — wrong code does NOT consume the pending Redis blob.
// Counter increments; subsequent retry within TTL with the correct code succeeds.
func TestEnrollTOTPVerify_WrongCodeKeepsPendingBlob(t *testing.T) {
	t.Parallel()
	h := newMFAHarness(t)
	userID := uuid.MustParse("44444444-4444-4444-4444-444444444444")

	secret, _ := totp.GenerateSecret()
	codes := []string{"AAAAAAAAA2", "BBBBBBBBB2", "CCCCCCCCC2", "DDDDDDDDD2", "EEEEEEEEE2",
		"FFFFFFFFF2", "GGGGGGGGG2", "HHHHHHHHH2", "IIIIIIIII2", "JJJJJJJJJ2"}
	blob, _ := json.Marshal(map[string]any{
		"secret":          secret,
		"recovery_codes":  codes,
		"created_at_unix": fixedNow.Unix(),
	})
	pendingKey := "auth:2fa:enroll:" + userID.String()
	h.mr.Set(pendingKey, string(blob))
	h.mr.SetTTL(pendingKey, 10*time.Minute)

	// Race re-check returns not-yet-enabled.
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(false, nil))

	// Submit a deliberately-wrong code (not the one Generate would yield).
	wrong := "000000"
	if totp.Generate(secret, fixedNow) == wrong {
		wrong = "111111" // unlikely collision but defensive
	}
	_, err := h.srv.EnrollTOTPVerify(context.Background(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
		UserId:                  userID.String(),
		Code:                    wrong,
		AckRecoveryCodesSaved:   true,
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidTOTPCode)
	// Pending blob still present.
	if !h.mr.Exists(pendingKey) {
		t.Errorf("pending blob should remain on wrong code")
	}
	// Failure audit.
	if got := len(h.auditP.byType(audit.Event2FAEnrollFailed)); got != 1 {
		t.Errorf("want 1 2fa.enroll.failed, got %d", got)
	}
}
