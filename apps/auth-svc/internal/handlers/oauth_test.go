// oauth_test.go — Story 2.3 P5 handler tests for BeginOAuth + CompleteOAuth.
//
// The unit-level decision logic is covered in:
//   - oauth/state_test.go     — state + PKCE
//   - oauth/google_test.go    — Google client
//   - oauth/github_test.go    — GitHub client
//   - oauth/linking_test.go   — 10-branch matrix
//
// This file pins the handler-level stitching: Connect-go error code +
// status string mapping, audit emission, 2FA hook path, JWT issuance
// integration with Story 2.2 jwt pkg.
//
// File→scenario mapping:
//   - 2.3-UNIT-050..055 (handler-level happy + rejection paths)
//   - 2.3-INT-019..024 (full state→provider→linking→JWT flow at unit level
//     via fakes; P9 will exercise the same flow with testcontainers)
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
)

// -- Test doubles ----------------------------------------------------------

type fakeOAuthProvider struct {
	authorizeURL string
	subject      string
	email        string
	exchangeErr  error
}

func (f *fakeOAuthProvider) BuildAuthorizeURL(stateID, _challenge, _locale string) string {
	if f.authorizeURL != "" {
		return f.authorizeURL
	}
	return "https://provider.example.com/authorize?state=" + stateID
}
func (f *fakeOAuthProvider) ExchangeCode(_ context.Context, _code, _verifier string) (string, string, error) {
	if f.exchangeErr != nil {
		return "", "", f.exchangeErr
	}
	return f.subject, f.email, nil
}

type fakeLinker struct {
	outcome oauthpkg.LinkOutcome
	err     error
}

func (f *fakeLinker) DecideAndLink(_ context.Context, _provider, _subject, _email, _locale string) (oauthpkg.LinkOutcome, error) {
	if f.err != nil {
		return oauthpkg.LinkOutcome{}, f.err
	}
	return f.outcome, nil
}

type fakeOAuthJWT struct {
	access            string
	refresh           string
	signErr           error
	lastWasLocked     bool
	lastWasLockedSeen bool
}

func (f *fakeOAuthJWT) SignAccessToken(_ uuid.UUID, _ time.Time) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}
	if f.access == "" {
		return "fake-access-jwt.eyJqdGkiOiJhY2Nlc3MtanRpIn0.sig", nil
	}
	return f.access, nil
}
func (f *fakeOAuthJWT) SignAccessTokenWithLockBypass(uid uuid.UUID, now time.Time, wasLocked bool) (string, error) {
	f.lastWasLocked = wasLocked
	f.lastWasLockedSeen = true
	return f.SignAccessToken(uid, now)
}
func (f *fakeOAuthJWT) SignRefreshToken(_ uuid.UUID, _ uuid.UUID, _ time.Time) (string, error) {
	if f.signErr != nil {
		return "", f.signErr
	}
	if f.refresh == "" {
		return "fake-refresh-jwt.eyJqdGkiOiJyZWZyZXNoLWp0aSJ9.sig", nil
	}
	return f.refresh, nil
}

type collectingPublisher struct {
	events []audit.Event
}

func (p *collectingPublisher) Publish(_ context.Context, e audit.Event) error {
	p.events = append(p.events, e)
	return nil
}

// newOAuthServer builds an AuthServer wired for OAuth-only handler tests.
// The state service is a real `oauthpkg.Service` backed by miniredis (this
// is the simplest way to round-trip a real state_id through ConsumeState).
func newOAuthServer(t *testing.T, opts ...func(*AuthServer)) (*AuthServer, *collectingPublisher) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	pub := &collectingPublisher{}
	srv := AuthServer{
		Redis:        rdb,
		Audit:        pub,
		JWT:          &fakeOAuthJWT{},
		Clock:        func() time.Time { return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC) },
		OAuthState:   oauthpkg.NewService(rdb),
		OAuthGoogle:  &fakeOAuthProvider{subject: "google-sub-1", email: "alice@example.com"},
		OAuthGithub:  &fakeOAuthProvider{subject: "github-987654321", email: "dev@example.com"},
		OAuthLinking: &fakeLinker{outcome: oauthpkg.LinkOutcome{UserID: uuid.New(), Branch: oauthpkg.BranchCNewUser, IsNewUser: true}},
	}
	for _, opt := range opts {
		opt(&srv)
	}
	return NewAuthServer(srv), pub
}

// -- BeginOAuth -----------------------------------------------------------

// Scenario: 2.3-UNIT-050
// BeginOAuth happy path — returns authorize_url, state_id, expires_at;
// emits audit.EventOAuthInitiate.
func TestBeginOAuth_HappyPath(t *testing.T) {
	t.Parallel()
	s, pub := newOAuthServer(t)
	resp, err := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		ReturnTo:  "https://console.he-api.com/en/dashboard",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	if resp.Msg.GetAuthorizeUrl() == "" {
		t.Error("AuthorizeUrl empty")
	}
	if resp.Msg.GetStateId() == "" {
		t.Error("StateId empty")
	}
	if resp.Msg.GetExpiresAtUnix() == 0 {
		t.Error("ExpiresAtUnix zero")
	}
	// Audit emitted exactly once.
	gotEvents := 0
	for _, e := range pub.events {
		if e.EventType == audit.EventOAuthInitiate {
			gotEvents++
		}
	}
	if gotEvents != 1 {
		t.Errorf("audit.EventOAuthInitiate count = %d, want 1", gotEvents)
	}
}

// Scenario: 2.3-UNIT-051
// BeginOAuth rejects unknown provider with StatusOAuthInvalidProvider.
func TestBeginOAuth_InvalidProvider(t *testing.T) {
	t.Parallel()
	s, _ := newOAuthServer(t)
	_, err := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider: "facebook",
	}))
	if err == nil {
		t.Fatal("BeginOAuth: want error on invalid provider, got nil")
	}
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want CodeInvalidArgument", cerr)
	}
}

// -- CompleteOAuth --------------------------------------------------------

// Scenario: 2.3-UNIT-052
// CompleteOAuth happy path Branch C — token exchange + linking decision
// returns BranchCNewUser → access + refresh tokens emitted, audit success
// event emitted.
func TestCompleteOAuth_HappyPathBranchC(t *testing.T) {
	t.Parallel()
	newUID := uuid.New()
	s, pub := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthLinking = &fakeLinker{outcome: oauthpkg.LinkOutcome{
			UserID:    newUID,
			Email:     "alice@example.com",
			Branch:    oauthpkg.BranchCNewUser,
			IsNewUser: true,
		}}
	})
	// First, run BeginOAuth to materialize a real state in Redis.
	beginResp, err := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		ReturnTo:  "https://console.he-api.com/en/dashboard",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	// Then CompleteOAuth using that state_id.
	resp, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code-abc",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if resp.Msg.GetUserId() != newUID.String() {
		t.Errorf("UserId = %q, want %q", resp.Msg.GetUserId(), newUID.String())
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Error("AccessToken empty")
	}
	if resp.Msg.GetRefreshToken() == "" {
		t.Error("RefreshToken empty")
	}
	if !resp.Msg.GetIsNewUser() {
		t.Error("IsNewUser=false on Branch C")
	}
	if got := resp.Msg.GetLinkOutcome(); got != authv1.LinkOutcome_LINK_OUTCOME_NEW_USER {
		t.Errorf("LinkOutcome = %v, want LINK_OUTCOME_NEW_USER", got)
	}
	if resp.Msg.GetRequires_2Fa() {
		t.Error("Requires_2Fa=true on non-MFA user")
	}
	if resp.Msg.GetReturnTo() != "https://console.he-api.com/en/dashboard" {
		t.Errorf("ReturnTo = %q, want passthrough from state", resp.Msg.GetReturnTo())
	}
	// Verify callback.success audit emitted.
	successCount := 0
	for _, e := range pub.events {
		if e.EventType == audit.EventOAuthCallbackSuccess {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("audit.EventOAuthCallbackSuccess count = %d, want 1", successCount)
	}
}

// Scenario: 2.3-UNIT-053 + 2.3-SEC-001 (replay defence at handler boundary)
// CompleteOAuth with a state_id never issued → StatusOAuthStateInvalid
// (anti-info-leak; same response as expired or provider-mismatch).
func TestCompleteOAuth_UnknownStateID(t *testing.T) {
	t.Parallel()
	s, pub := newOAuthServer(t)
	_, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   "no-such-state-id-43chars-padding-aaaaaaaaaa",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *connect.Error", err)
	}
	if cerr.Code() != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want CodeInvalidArgument", cerr.Code())
	}
	if cerr.Message() != StatusOAuthStateInvalid {
		t.Errorf("message = %q, want %q", cerr.Message(), StatusOAuthStateInvalid)
	}
	// error_state audit emitted.
	found := false
	for _, e := range pub.events {
		if e.EventType == audit.EventOAuthCallbackErrState {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit.EventOAuthCallbackErrState not emitted")
	}
}

// Scenario: 2.3-UNIT-054
// Provider returns ErrEmailNotVerified → handler emits 400_oauth_email_not_verified.
func TestCompleteOAuth_ProviderEmailNotVerified(t *testing.T) {
	t.Parallel()
	s, _ := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthGoogle = &fakeOAuthProvider{exchangeErr: oauthpkg.ErrEmailNotVerified}
	})
	beginResp, _ := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	_, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *connect.Error", err)
	}
	if cerr.Message() != StatusOAuthEmailNotVerified {
		t.Errorf("message = %q, want %q", cerr.Message(), StatusOAuthEmailNotVerified)
	}
}

// Scenario: 2.3-UNIT-055 + BR-3.5 (2FA hook)
// CompleteOAuth Branch A user with totp_enabled=true → Requires_2Fa=true,
// no access/refresh tokens issued.
func TestCompleteOAuth_RequiresMFA(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	s, _ := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthLinking = &fakeLinker{outcome: oauthpkg.LinkOutcome{
			UserID:      uid,
			Email:       "alice@example.com",
			Branch:      oauthpkg.BranchARelogin,
			RequiresMFA: true,
		}}
	})
	beginResp, _ := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	resp, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if !resp.Msg.GetRequires_2Fa() {
		t.Error("Requires_2Fa = false despite RequiresMFA outcome")
	}
	if resp.Msg.GetAccessToken() != "" {
		t.Error("AccessToken issued on requires_2fa path")
	}
	if resp.Msg.GetRefreshToken() != "" {
		t.Error("RefreshToken issued on requires_2fa path")
	}
}

// Scenario: 2.3-UNIT-056 + Branch B.2 anti-takeover
// Linking returns ErrLinkUnverified → handler emits StatusOAuthLinkUnverified.
func TestCompleteOAuth_LinkUnverified(t *testing.T) {
	t.Parallel()
	s, pub := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthLinking = &fakeLinker{err: oauthpkg.ErrLinkUnverified}
	})
	beginResp, _ := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	_, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *connect.Error", err)
	}
	if cerr.Message() != StatusOAuthLinkUnverified {
		t.Errorf("message = %q, want %q", cerr.Message(), StatusOAuthLinkUnverified)
	}
	// link.rejected_unverified audit emitted.
	found := false
	for _, e := range pub.events {
		if e.EventType == audit.EventOAuthLinkRejUnverified {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit.EventOAuthLinkRejUnverified not emitted")
	}
}

// Scenario: 2.3-UNIT-057
// Linking returns ErrAuthenticationFailed (pending_deletion anti-enum)
// → handler emits StatusInvalidCredentials (identical to wrong-password).
func TestCompleteOAuth_AntiEnumPendingDeletion(t *testing.T) {
	t.Parallel()
	s, _ := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthLinking = &fakeLinker{err: oauthpkg.ErrAuthenticationFailed}
	})
	beginResp, _ := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	_, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *connect.Error", err)
	}
	if cerr.Code() != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want CodeUnauthenticated (anti-enum)", cerr.Code())
	}
	if cerr.Message() != StatusInvalidCredentials {
		t.Errorf("message = %q, want %q (anti-enum)", cerr.Message(), StatusInvalidCredentials)
	}
}

// Scenario: 2.3-UNIT-058 + BR-3.10 + m-3
// Locked-bypass audit event emitted when outcome.WasLocked=true.
func TestCompleteOAuth_LockedBypassAudit(t *testing.T) {
	t.Parallel()
	uid := uuid.New()
	s, pub := newOAuthServer(t, func(s *AuthServer) {
		s.OAuthLinking = &fakeLinker{outcome: oauthpkg.LinkOutcome{
			UserID:    uid,
			Email:     "locked@example.com",
			Branch:    oauthpkg.BranchARelogin,
			WasLocked: true,
		}}
	})
	beginResp, _ := s.BeginOAuth(context.Background(), connect.NewRequest(&authv1.BeginOAuthRequest{
		Provider:  "google",
		Locale:    "en",
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	resp, err := s.CompleteOAuth(context.Background(), connect.NewRequest(&authv1.CompleteOAuthRequest{
		Provider:  "google",
		Code:      "code",
		StateId:   beginResp.Msg.GetStateId(),
		ClientIp:  "203.0.113.4",
		UserAgent: "ua-test",
	}))
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Error("AccessToken missing on bypass path")
	}
	// lock_bypass audit emitted.
	found := false
	for _, e := range pub.events {
		if e.EventType == audit.EventOAuthLockBypass {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit.EventOAuthLockBypass not emitted on WasLocked outcome")
	}
}
