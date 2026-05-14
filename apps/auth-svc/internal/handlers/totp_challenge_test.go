package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
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
	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

// --- fake MFA signer + parser -------------------------------------------

// fakeMFASigner generates an "mfa-token-N" string + matching JTI; ParseMFAToken
// looks it up against the in-memory map (mimics a JWT issuer + verifier in a
// single struct so tests can issue + parse without RSA crypto).
type fakeMFASigner struct {
	mu        sync.Mutex
	tokens    map[string]*handlers.MFAParsedClaims // key = signed token string
	seq       int
}

func newFakeMFASigner() *fakeMFASigner {
	return &fakeMFASigner{tokens: make(map[string]*handlers.MFAParsedClaims)}
}

func (f *fakeMFASigner) IssueMFAToken(in handlers.MFAIssueInput, now time.Time) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	tok := "mfa-token-" + itoa(f.seq)
	jti := "mfa-jti-" + itoa(f.seq)
	f.tokens[tok] = &handlers.MFAParsedClaims{
		Subject:       in.UserID.String(),
		JTI:           jti,
		Audience:      "he-api",
		Purpose:       "2fa_challenge",
		LoginMethod:   in.LoginMethod,
		IPHash:        in.IPHash,
		UserAgentHash: in.UserAgentHash,
		ReturnTo:      in.ReturnTo,
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(5 * time.Minute).Unix(),
	}
	return tok, jti, nil
}

func (f *fakeMFASigner) ParseMFAToken(tok string) (*handlers.MFAParsedClaims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.tokens[tok]
	if !ok {
		return nil, errFakeMFANotFound
	}
	return c, nil
}

var errFakeMFANotFound = stringErr("fake mfa: not found")

type stringErr string

func (e stringErr) Error() string { return string(e) }

// --- challenge harness --------------------------------------------------

type challengeHarness struct {
	srv      *handlers.AuthServer
	mock     pgxmock.PgxConnIface
	mr       *miniredis.Miniredis
	rdb      *redis.Client
	notif    *fakeNotification
	auditP   *recordingAudit
	mfa      *fakeMFASigner
	jwt      *fakeJWT
	kms      kms.KMSClient
}

func newChallengeHarness(t *testing.T) *challengeHarness {
	t.Helper()
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet pgx: %v", err)
		}
		mock.Close(context.Background())
	})
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	h := &challengeHarness{
		mock:   mock,
		mr:     mr,
		rdb:    rdb,
		notif:  &fakeNotification{},
		auditP: &recordingAudit{},
		mfa:    newFakeMFASigner(),
		jwt:    &fakeJWT{},
		kms:    kms.NewNoOp(),
	}
	h.srv = handlers.NewAuthServer(handlers.AuthServer{
		DB:             mock,
		DBTx:           mock,
		Redis:          rdb,
		Notification:   h.notif,
		Audit:          h.auditP,
		JWT:            h.jwt,
		Clock:          func() time.Time { return fixedNow },
		ConsoleBaseURL: "https://console.he-api.com",
		Logger:         slog.Default(),
		KMS:            h.kms,
		MFASigner:      h.mfa,
		MFAParser:      h.mfa,
		Issuer:         "He-API",
	})
	return h
}

// --- LoginUser 2FA branch tests -----------------------------------------

// Scenario: 2.4-UNIT-039 — LoginUser with totp_enabled=TRUE issues mfa_token +
// writes JTI to Redis + returns REQUIRES_2FA (no access/refresh tokens).
func TestLoginUser_RequiresTwoFAReturnsMFAToken(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	verifiedAt := time.Now().Add(-time.Hour)
	pwHash := mustHash(t, "correct horse battery staple")

	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE email = \$1`).
		WithArgs("user@example.com").
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", nil, true /* totp_enabled */, "active", nil, nil,
			time.Now(), time.Now(),
		))

	resp, err := h.srv.LoginUser(context.Background(), connect.NewRequest(&authv1.LoginUserRequest{
		Email:     "user@example.com",
		Password:  "correct horse battery staple",
		ClientIp:  "1.2.3.4",
		UserAgent: "Mozilla/5.0",
	}))
	if err != nil {
		t.Fatalf("LoginUser: %v", err)
	}
	if resp.Msg.GetStatus() != authv1.LoginStatus_LOGIN_STATUS_REQUIRES_2FA {
		t.Errorf("status = %v, want REQUIRES_2FA", resp.Msg.GetStatus())
	}
	if resp.Msg.GetMfaToken() == "" {
		t.Errorf("mfa_token empty")
	}
	// Access/refresh MUST be empty.
	if resp.Msg.GetAccessToken() != "" {
		t.Errorf("access_token issued before 2FA — should be empty")
	}
	if resp.Msg.GetRefreshToken() != "" {
		t.Errorf("refresh_token issued before 2FA — should be empty")
	}
	// JTI was registered in Redis.
	// Issued JTI shape: "mfa-jti-N" per fakeMFASigner. Lookup via the same
	// challengeJTIKey helper indirectly: redis stores under sha256-hashed
	// key, so we can't grep by raw jti. Instead assert at least one key
	// exists under the prefix.
	hasPending := false
	for _, k := range h.mr.Keys() {
		if startsWith(k, "auth:2fa:challenge:") {
			hasPending = true
			break
		}
	}
	if !hasPending {
		t.Errorf("no JTI registry entry in redis")
	}
}

// --- ChallengeTOTP tests -------------------------------------------------

// helper: stage a pending JTI in redis and have the fake mfa signer parse
// the corresponding token to the right claims.
func stageMFAToken(t *testing.T, h *challengeHarness, userID uuid.UUID, ip, ua, returnTo string) (token string) {
	t.Helper()
	tok, _, err := h.mfa.IssueMFAToken(handlers.MFAIssueInput{
		UserID:        userID,
		LoginMethod:   "password",
		IPHash:        oauthHashIP(ip),
		UserAgentHash: oauthHashUA(ua),
		ReturnTo:      returnTo,
	}, fixedNow)
	if err != nil {
		t.Fatalf("issue mfa: %v", err)
	}
	// Parse claims to get JTI; then write to redis under the canonical key.
	claims, _ := h.mfa.ParseMFAToken(tok)
	keyer := challengeJTIRedisKey(claims.JTI)
	h.mr.Set(keyer, `{"user_id":"`+userID.String()+`"}`)
	h.mr.SetTTL(keyer, 5*time.Minute)
	return tok
}

// Scenario: 2.4-UNIT-046 — happy path: valid mfa_token + correct 6-digit code +
// matching IP/UA binding → access+refresh + aal=2 + JTI DEL'd.
func TestChallengeTOTP_HappyPath(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	ip, ua := "1.2.3.4", "Mozilla/5.0"

	// Stage TOTP secret in mock PG (NoOp KMS → secret stored verbatim).
	secret, _ := totp.GenerateSecret()
	// NoOp KMS encrypt = identity; base64 wrap done by repository layer.
	enc := base64Encode(secret)
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))
	// MarkTOTPUsed UPDATE.
	h.mock.ExpectExec(`UPDATE he_api\.users SET totp_last_used_at=NOW`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	tok := stageMFAToken(t, h, userID, ip, ua, "/dashboard")
	code := totp.Generate(secret, fixedNow)

	resp, err := h.srv.ChallengeTOTP(context.Background(), connect.NewRequest(&authv1.ChallengeTOTPRequest{
		MfaToken:  tok,
		Code:      code,
		ClientIp:  ip,
		UserAgent: ua,
	}))
	if err != nil {
		t.Fatalf("ChallengeTOTP: %v", err)
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Errorf("access_token empty")
	}
	if resp.Msg.GetAal() != 2 {
		t.Errorf("aal = %d, want 2", resp.Msg.GetAal())
	}
	if resp.Msg.GetReturnTo() != "/dashboard" {
		t.Errorf("return_to = %q want /dashboard", resp.Msg.GetReturnTo())
	}
	// JTI consumed: no challenge keys remain.
	for _, k := range h.mr.Keys() {
		if startsWith(k, "auth:2fa:challenge:") {
			t.Errorf("JTI key still present: %s", k)
		}
	}
	// Audit success emitted.
	if got := len(h.auditP.byType(audit.Event2FAChallengeSuccess)); got != 1 {
		t.Errorf("want 1 challenge success audit, got %d", got)
	}
}

// Scenario: 2.4-UNIT-043 — mfa_token whose JTI is not in Redis (consumed or
// expired) → 401_mfa_token_invalid.
func TestChallengeTOTP_JTIMissing(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	// Issue a token but DO NOT stage the JTI in redis.
	tok, _, _ := h.mfa.IssueMFAToken(handlers.MFAIssueInput{
		UserID:        userID,
		IPHash:        oauthHashIP("1.2.3.4"),
		UserAgentHash: oauthHashUA("ua"),
	}, fixedNow)

	_, err := h.srv.ChallengeTOTP(context.Background(), connect.NewRequest(&authv1.ChallengeTOTPRequest{
		MfaToken:  tok,
		Code:      "123456",
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusMFATokenInvalid)
}

// Scenario: 2.4-UNIT-044 — IP/UA binding mismatch → 401_mfa_token_binding_mismatch
// + HIGH-severity audit `2fa.challenge.binding_failed`.
func TestChallengeTOTP_BindingMismatch(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.New()
	// Issue against ip A; submit from ip B (across /24 boundary).
	tok := stageMFAToken(t, h, userID, "1.2.3.4", "ua", "")
	_, err := h.srv.ChallengeTOTP(context.Background(), connect.NewRequest(&authv1.ChallengeTOTPRequest{
		MfaToken:  tok,
		Code:      "123456",
		ClientIp:  "9.9.9.9", // different /24
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusMFATokenBindingMismatch)
	if got := len(h.auditP.byType(audit.Event2FAChallengeBinding)); got != 1 {
		t.Errorf("want 1 binding_failed audit, got %d", got)
	}
}

// Scenario: 2.4-UNIT-049 — wrong 6-digit code does NOT delete JTI (allow
// retry within mfa_token TTL up to ratelimit ceiling).
func TestChallengeTOTP_WrongCodeKeepsJTI(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	secret, _ := totp.GenerateSecret()
	enc := base64Encode(secret)
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	tok := stageMFAToken(t, h, userID, "1.2.3.4", "ua", "")
	// Determine wrong code.
	right := totp.Generate(secret, fixedNow)
	wrong := "000000"
	if right == wrong {
		wrong = "111111"
	}
	_, err := h.srv.ChallengeTOTP(context.Background(), connect.NewRequest(&authv1.ChallengeTOTPRequest{
		MfaToken:  tok,
		Code:      wrong,
		ClientIp:  "1.2.3.4",
		UserAgent: "ua",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidTOTPCode)
	// JTI still in redis.
	hasJTI := false
	for _, k := range h.mr.Keys() {
		if startsWith(k, "auth:2fa:challenge:") {
			hasJTI = true
			break
		}
	}
	if !hasJTI {
		t.Errorf("JTI should remain on wrong code")
	}
}

// --- helpers --------------------------------------------------------------

// oauthHashIP / oauthHashUA delegate to the Story 2.3 helpers so the test's
// pre-staged IP/UA hash matches the handler's runtime computation.
func oauthHashIP(ip string) string { return oauth.HashClientIP(ip) }
func oauthHashUA(ua string) string { return oauth.HashUserAgent(ua) }

// challengeJTIRedisKey mirrors handlers.challengeJTIKey (unexported in the
// handlers package). Inlined here so the test can stage the JTI registry
// entry at the same key the handler reads.
func challengeJTIRedisKey(jti string) string {
	sum := sha256.Sum256([]byte(jti))
	return "auth:2fa:challenge:" + hex.EncodeToString(sum[:])
}

func base64Encode(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// JSON helper kept in case future tests need it.
var _ = json.Marshal

// startsWith tests prefix match.
func startsWith(s, p string) bool {
	if len(s) < len(p) {
		return false
	}
	return s[:len(p)] == p
}
