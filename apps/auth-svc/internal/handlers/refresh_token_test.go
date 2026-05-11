package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	authjwt "github.com/he-api/he-api/apps/auth-svc/internal/jwt"
)

// fakeVerifier is a deterministic JWTVerifier used by RefreshToken tests.
// It returns the configured claims (or err) regardless of the actual
// token bytes — tests just need to exercise the rotation logic, not the
// crypto.
type fakeVerifier struct {
	claims *authjwt.Claims
	err    error
}

func (f *fakeVerifier) Verify(_ string) (*authjwt.Claims, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.claims, nil
}

func newRefreshHarness(t *testing.T, verifier *fakeVerifier) (*harness, *fakeJWT) {
	t.Helper()
	h := newHarness(t)
	fj := &fakeJWT{}
	h.srv = handlers.NewAuthServer(handlers.AuthServer{
		DB:             h.mock,
		Redis:          h.rdb,
		HIBP:           h.hibp,
		Notification:   h.notif,
		Audit:          h.auditP,
		JWT:            fj,
		JWTVerify:      verifier,
		Clock:          func() time.Time { return fixedNow },
		ConsoleBaseURL: "https://console.he-api.com",
		Logger:         h.srv.Logger,
	})
	return h, fj
}

// Scenario: 2.2-UNIT-118
// Refresh rotation atomicity: presented jti matches the stored tracker →
// Lua script DEL's old + SET's new in one atomic command. Handler returns
// new access + refresh tokens. The Redis tracker now carries the freshly
// signed jti.
func TestRefreshToken_HappyRotation(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	familyID := uuid.New()
	oldJTI := "old-refresh-jti-abc"
	verifier := &fakeVerifier{claims: &authjwt.Claims{
		Subject:   userID.String(),
		IssuedAt:  fixedNow.Add(-time.Hour).Unix(),
		ExpiresAt: fixedNow.Add(29 * 24 * time.Hour).Unix(),
		JTI:       oldJTI,
		Audience:  authjwt.Audience,
		FamilyID:  familyID.String(),
	}}
	h, fj := newRefreshHarness(t, verifier)

	// Seed the family tracker with the old jti.
	familyKey := "auth:refresh:" + familyID.String()
	if err := h.rdb.Set(context.Background(), familyKey, oldJTI, 30*24*time.Hour).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "any-token-bytes-fake-verifier-doesn't-look",
		ClientIp:     "1.2.3.4",
	}))
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
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
	// Redis tracker now carries the new jti (different from old).
	gotJTI, err := h.mr.Get(familyKey)
	if err != nil {
		t.Fatalf("Redis family tracker missing after rotation: %v", err)
	}
	if gotJTI == oldJTI {
		t.Errorf("Redis tracker still carries old jti after rotation")
	}
	if fj.refreshSeq == 0 {
		t.Errorf("Signer did not issue a new refresh token")
	}

	// Audit signin_success with refresh.rotated.
	rotated := 0
	for _, e := range h.auditP.events {
		if e.EventType == audit.EventSigninSuccess && e.ErrorCode == "refresh.rotated" {
			rotated++
		}
	}
	if rotated != 1 {
		t.Errorf("audit refresh.rotated events = %d, want 1; got %+v", rotated, h.auditP.events)
	}
}

// Scenario: 2.2-UNIT-119
// Reuse detection: presented jti != stored. The Lua script DEL's the
// family entirely + handler returns 401 + audits refresh.reuse_detected.
// A subsequent legitimate refresh in the same family ALSO fails (because
// the family was revoked).
func TestRefreshToken_ReuseDetectedRevokesFamily(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	familyID := uuid.New()
	presentedJTI := "stale-jti-replayed-by-attacker"
	storedJTI := "current-jti-already-rotated-past-this-one"
	verifier := &fakeVerifier{claims: &authjwt.Claims{
		Subject:  userID.String(),
		JTI:      presentedJTI,
		FamilyID: familyID.String(),
		Audience: authjwt.Audience,
	}}
	h, _ := newRefreshHarness(t, verifier)
	familyKey := "auth:refresh:" + familyID.String()
	if err := h.rdb.Set(context.Background(), familyKey, storedJTI, 30*24*time.Hour).Err(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "any",
		ClientIp:     "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)

	// Family tracker MUST be DEL'd — even the legitimate current token
	// can no longer rotate.
	if h.mr.Exists(familyKey) {
		t.Errorf("family key still exists after reuse detection — should be DEL'd")
	}
	// Audit captures the distinguishing detail.
	var reuseEvents int
	for _, e := range h.auditP.events {
		if e.ErrorCode == "refresh.reuse_detected" {
			reuseEvents++
		}
	}
	if reuseEvents != 1 {
		t.Errorf("audit refresh.reuse_detected count = %d, want 1; got %+v", reuseEvents, h.auditP.events)
	}
}

// Missing family tracker (revoked OR never tracked OR Redis lost) →
// 401 + audit refresh.family_revoked. The handler can't tell why the
// tracker is missing; the response is generic.
func TestRefreshToken_MissingFamilyReturns401(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	familyID := uuid.New()
	verifier := &fakeVerifier{claims: &authjwt.Claims{
		Subject:  userID.String(),
		JTI:      "doesn't matter",
		FamilyID: familyID.String(),
		Audience: authjwt.Audience,
	}}
	h, _ := newRefreshHarness(t, verifier)

	_, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "any",
		ClientIp:     "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)

	var revokedEvents int
	for _, e := range h.auditP.events {
		if e.ErrorCode == "refresh.family_revoked" {
			revokedEvents++
		}
	}
	if revokedEvents != 1 {
		t.Errorf("audit refresh.family_revoked count = %d, want 1", revokedEvents)
	}
}

// JWT verification failure (signature / expiry / alg) → 401.
func TestRefreshToken_InvalidJWTReturns401(t *testing.T) {
	t.Parallel()
	verifier := &fakeVerifier{err: errors.New("token expired")}
	h, _ := newRefreshHarness(t, verifier)

	_, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "anything",
		ClientIp:     "1.2.3.4",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
}

// Empty refresh_token → 401 short-circuit (no verifier call).
func TestRefreshToken_EmptyRefreshTokenReturns401(t *testing.T) {
	t.Parallel()
	verifier := &fakeVerifier{} // would explode if called
	h, _ := newRefreshHarness(t, verifier)
	_, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
}

// Defensive: an access token (no `fam` claim) presented as refresh →
// 401. This catches caller bugs where the wrong token is forwarded.
func TestRefreshToken_AccessTokenPresentedAsRefresh(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	// Missing FamilyID — characteristic of an access token.
	verifier := &fakeVerifier{claims: &authjwt.Claims{
		Subject:  userID.String(),
		JTI:      "access-jti",
		Audience: authjwt.Audience,
	}}
	h, _ := newRefreshHarness(t, verifier)
	_, err := h.srv.RefreshToken(context.Background(), connect.NewRequest(&authv1.RefreshTokenRequest{
		RefreshToken: "any",
	}))
	assertConnectStatus(t, err, connect.CodeUnauthenticated, handlers.StatusInvalidCredentials)
}
