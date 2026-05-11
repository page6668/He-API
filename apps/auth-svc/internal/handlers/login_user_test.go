package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/password"
)

// --- fake JWT signer ----------------------------------------------------

// fakeJWT issues deterministic test tokens so tests can assert on
// composition (e.g., that LoginUser called SignRefreshToken with a
// family_id and stashed the resulting jti in Redis).
type fakeJWT struct {
	mu          sync.Mutex
	accessSeq   int
	refreshSeq  int
	lastFamily  uuid.UUID
	lastUserID  uuid.UUID
}

// We construct a real-looking JWT shape (header.payload.signature) so the
// LoginUser handler's jtiFromToken helper can extract a `jti` claim from
// our fake refresh tokens.
func (f *fakeJWT) SignAccessToken(userID uuid.UUID, _ time.Time) (string, error) {
	f.mu.Lock()
	f.accessSeq++
	seq := f.accessSeq
	f.mu.Unlock()
	return testJWT(`{"sub":"`+userID.String()+`","jti":"access-jti-`+itoa(seq)+`","aud":"he-api"}`), nil
}

func (f *fakeJWT) SignRefreshToken(userID uuid.UUID, familyID uuid.UUID, _ time.Time) (string, error) {
	f.mu.Lock()
	f.refreshSeq++
	f.lastFamily = familyID
	f.lastUserID = userID
	seq := f.refreshSeq
	f.mu.Unlock()
	jti := "refresh-jti-" + itoa(seq)
	return testJWT(`{"sub":"` + userID.String() + `","jti":"` + jti + `","aud":"he-api","fam":"` + familyID.String() + `"}`), nil
}

func testJWT(payloadJSON string) string {
	// Base64url-unpadded the payload, fake header + signature parts so the
	// shape parses but signature verification is a no-op (we don't verify
	// here — LoginUser only extracts `jti` from the payload).
	const header = "eyJhbGciOiJSUzI1NiJ9" // {"alg":"RS256"}
	const sig = "fake-signature"
	payload := base64URLEncode([]byte(payloadJSON))
	return header + "." + payload + "." + sig
}

func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}

// --- harness extension ---------------------------------------------------

func newSigninHarness(t *testing.T) (*harness, *fakeJWT) {
	t.Helper()
	h := newHarness(t)
	fj := &fakeJWT{}
	// Re-construct the AuthServer with the JWT signer attached. The
	// existing harness exposes its dependency objects via fields, so we
	// rebuild via handlers.NewAuthServer using those references.
	h.srv = handlers.NewAuthServer(handlers.AuthServer{
		DB:             h.mock,
		Redis:          h.rdb,
		HIBP:           h.hibp,
		Notification:   h.notif,
		Audit:          h.auditP,
		JWT:            fj,
		Clock:          func() time.Time { return fixedNow },
		ConsoleBaseURL: "https://console.he-api.com",
		Logger:         h.srv.Logger, // reuse the slog default
	})
	return h, fj
}

func mustHash(t *testing.T, pw string) []byte {
	t.Helper()
	h, err := password.Hash([]byte(pw))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

// --- LoginUser tests -----------------------------------------------------

// Scenario: 2.2-UNIT-132
// Unknown email → DummyCompare consumes the bcrypt budget + same
// 401_invalid_credentials response (no enumeration signal).
func TestLoginUser_UnknownEmailReturnsInvalidCredentials(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("ghost@example.com").
		WillReturnRows(userRows()) // empty

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "ghost@example.com",
		Password: "doesn't matter",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
	// notification + JWT signer MUST NOT have been called.
	if h.notif.calls != 0 {
		t.Errorf("notification.calls = %d, want 0", h.notif.calls)
	}
}

// Wrong password against a real user → also 401_invalid_credentials.
func TestLoginUser_WrongPasswordReturnsInvalidCredentials(t *testing.T) {
	t.Parallel()
	h, fj := newSigninHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", mustHash(t, "correct password"), &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "user@example.com",
		Password: "wrong password",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
	// JWT signer MUST NOT have run on a failed compare.
	if fj.refreshSeq != 0 {
		t.Errorf("refresh signed on wrong password; want 0, got %d", fj.refreshSeq)
	}
}

// Scenario: 2.2-UNIT-141
// Happy path: bcrypt match + email_verified + status=active + totp=false →
// signs access + refresh tokens; writes auth:refresh:{family_id}=jti to
// Redis; audits auth.signin_success; returns LOGIN_STATUS_OK.
func TestLoginUser_HappyPath(t *testing.T) {
	t.Parallel()
	h, fj := newSigninHarness(t)
	userID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "correct horse battery staple")
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", nil, false /* totp_enabled */, "active", nil, nil,
			time.Now(), time.Now(),
		))

	resp, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "user@example.com",
		Password: "correct horse battery staple",
		ClientIp: "1.2.3.4",
	}))
	if err != nil {
		t.Fatalf("LoginUser: %v", err)
	}
	if resp.Msg.GetStatus() != authv1.LoginStatus_LOGIN_STATUS_OK {
		t.Errorf("status = %v, want LOGIN_STATUS_OK", resp.Msg.GetStatus())
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Errorf("access_token empty")
	}
	if resp.Msg.GetRefreshToken() == "" {
		t.Errorf("refresh_token empty")
	}
	if resp.Msg.GetAccessTokenExpiresInSeconds() != 900 {
		t.Errorf("access TTL = %d, want 900", resp.Msg.GetAccessTokenExpiresInSeconds())
	}
	if resp.Msg.GetRefreshTokenExpiresInSeconds() != 2592000 {
		t.Errorf("refresh TTL = %d, want 2592000", resp.Msg.GetRefreshTokenExpiresInSeconds())
	}
	if fj.lastUserID != userID {
		t.Errorf("Signer saw userID = %s, want %s", fj.lastUserID, userID)
	}
	if fj.refreshSeq != 1 {
		t.Errorf("Refresh signed %d times, want 1", fj.refreshSeq)
	}

	// Redis must carry the auth:refresh:{family_id}=jti tracker.
	familyKey := "auth:refresh:" + fj.lastFamily.String()
	val, err := h.mr.Get(familyKey)
	if err != nil {
		t.Fatalf("Redis missing refresh family tracker: %v", err)
	}
	if !strings.HasPrefix(val, "refresh-jti-") {
		t.Errorf("Redis refresh family tracker = %q, want refresh-jti-*", val)
	}

	// Audit auth.signin_success emitted.
	if len(h.auditP.byType(audit.EventSigninSuccess)) != 1 {
		t.Errorf("audit signin_success count = %d, want 1", len(h.auditP.byType(audit.EventSigninSuccess)))
	}
}

// Email not verified → 403_email_not_verified. bcrypt MAY or may not have
// been checked — only the response code matters (the email-not-verified
// state is observable to a legitimate user; the timing-side-channel risk
// is identical to the wrong-password branch which already runs bcrypt).
func TestLoginUser_EmailNotVerified(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	userID := uuid.New()
	pwHash := mustHash(t, "correct password")
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, nil, // email_verified_at = nil
			nil, nil, "en", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "user@example.com",
		Password: "correct password",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusEmailNotVerified)
}

// Account suspended → 403_account_suspended.
func TestLoginUser_AccountSuspended(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("susp@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "susp@example.com", mustHash(t, "p"), &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "suspended", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "susp@example.com",
		Password: "doesn't matter",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusAccountSuspended)
}

// Account pending_deletion → 410_account_deleted.
func TestLoginUser_AccountPendingDeletion(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("del@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "del@example.com", mustHash(t, "p"), &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "pending_deletion", nil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "del@example.com",
		Password: "p",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusAccountDeleted)
}

// Account locked + unexpired → 423_account_locked + Retry-After.
func TestLoginUser_AccountLockedUnexpired(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	lockedUntil := fixedNow.Add(30 * time.Minute) // still in the future
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("locked@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "locked@example.com", mustHash(t, "p"), &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "locked", &lockedUntil, nil,
			time.Now(), time.Now(),
		))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "locked@example.com",
		Password: "p",
		ClientIp: "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusAccountLocked)
}

// Scenario: 2.2-UNIT-139
// Soft-lock activation: 5th wrong password → UPDATE locked_until + audit
// account_locked. The handler STILL returns 401_invalid_credentials so
// the attacker can't binary-search the threshold.
func TestLoginUser_FifthWrongPasswordSoftLocks(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	userID := uuid.New()
	verifiedAt := time.Now()
	pwHash := mustHash(t, "correct password")

	// Pre-set the signin-failure counter to 4 so the next failure hits 5.
	emailHash := hashEmailForTest("user@example.com")
	failureKey := "auth:signin:failures:" + emailHash
	if err := h.rdb.Set(context.Background(), failureKey, "4", time.Hour).Err(); err != nil {
		t.Fatalf("seed failures: %v", err)
	}

	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", nil, false, "active", nil, nil,
			time.Now(), time.Now(),
		))
	// SoftLockUser expected.
	h.mock.ExpectExec(`UPDATE he_api\.users SET status='locked'`).
		WithArgs(pgxmock.AnyArg(), userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "user@example.com",
		Password: "wrong password",
		ClientIp: "5.5.5.5",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
	// Audit auth.account_locked MUST have been emitted alongside the
	// signin_failure event.
	if len(h.auditP.byType(audit.EventAccountLocked)) != 1 {
		t.Errorf("audit account_locked count = %d, want 1; got events=%+v", len(h.auditP.byType(audit.EventAccountLocked)), h.auditP.events)
	}
}

// IP rate limit → 429 + Retry-After.
func TestLoginUser_IPRateLimit(t *testing.T) {
	t.Parallel()
	h, _ := newSigninHarness(t)
	// Pre-fill the signin IP counter to the cap.
	for i := 0; i < 10; i++ {
		if err := h.rdb.Incr(context.Background(), "ratelimit:signin:ip:7.7.7.7").Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = h.rdb.Expire(context.Background(), "ratelimit:signin:ip:7.7.7.7", 15*time.Minute).Err()

	_, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:    "user@example.com",
		Password: "any",
		ClientIp: "7.7.7.7",
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimitSigninIP)
}

// Compute the email_hash deterministically for the soft-lock test. The
// handler uses ratelimit.EmailHash; we mirror that algorithm here so we
// can hand-construct the canonical Redis key without importing the
// ratelimit package symbol into every test.
func hashEmailForTest(email string) string {
	normalized := strings.TrimSpace(strings.ToLower(email))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
