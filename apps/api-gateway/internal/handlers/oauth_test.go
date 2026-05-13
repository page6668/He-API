// oauth_test.go — Story 2.3 P6 api-gateway OAuth HTTP handler tests.
//
// File→scenario mapping:
//   - 2.3-UNIT-061 + 2.3-SEC-002 (cookie binding — missing cookie rejects callback)
//   - 2.3-SEC-003 (cookie/query mismatch rejects callback)
//   - 2.3-UNIT-062 (initiate sets Set-Cookie + 302 to authorize URL)
//   - 2.3-UNIT-063 (callback success sets he_access + he_refresh + 302 to return_to)
//   - 2.3-UNIT-064 (provider=facebook → 400_oauth_invalid_provider via upstream)
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// -- fake upstream client --------------------------------------------------

type fakeOAuthUpstream struct {
	beginResp    *authv1.BeginOAuthResponse
	beginErr     error
	completeResp *authv1.CompleteOAuthResponse
	completeErr  error
	lastBegin    *authv1.BeginOAuthRequest
	lastComplete *authv1.CompleteOAuthRequest
}

func (f *fakeOAuthUpstream) BeginOAuth(_ context.Context, req *connect.Request[authv1.BeginOAuthRequest]) (*connect.Response[authv1.BeginOAuthResponse], error) {
	f.lastBegin = req.Msg
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return connect.NewResponse(f.beginResp), nil
}
func (f *fakeOAuthUpstream) CompleteOAuth(_ context.Context, req *connect.Request[authv1.CompleteOAuthRequest]) (*connect.Response[authv1.CompleteOAuthResponse], error) {
	f.lastComplete = req.Msg
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return connect.NewResponse(f.completeResp), nil
}

// Scenario: 2.3-UNIT-062
// Initiate happy path: sets he_oauth_state cookie, 302 to authorize URL.
func TestOAuthInitiate_HappyPath(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{
		beginResp: &authv1.BeginOAuthResponse{
			AuthorizeUrl: "https://accounts.google.com/o/oauth2/v2/auth?state=abc",
			StateId:      "state-id-abc-43chars-padding-aaaaaaaaaaaa",
		},
	}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate?return_to=/en/dashboard&locale=en", nil)
	req.Header.Set("User-Agent", "ua-test")
	req.Header.Set("X-Forwarded-For", "203.0.113.4")
	rec := httptest.NewRecorder()
	h.Initiate("google")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.google.com/") {
		t.Errorf("Location = %q, want google authorize URL", loc)
	}
	// he_oauth_state cookie set with the state_id.
	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == OAuthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("he_oauth_state cookie missing")
	}
	if stateCookie.Value != "state-id-abc-43chars-padding-aaaaaaaaaaaa" {
		t.Errorf("state cookie value = %q, want %q", stateCookie.Value, "state-id-abc-43chars-padding-aaaaaaaaaaaa")
	}
	if !stateCookie.HttpOnly {
		t.Error("state cookie not HttpOnly")
	}
	if !stateCookie.Secure {
		t.Error("state cookie not Secure in prod")
	}
	if stateCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie SameSite = %v, want Lax", stateCookie.SameSite)
	}
	if stateCookie.Path != OAuthStateCookiePath {
		t.Errorf("state cookie Path = %q, want %q", stateCookie.Path, OAuthStateCookiePath)
	}
	// Upstream got the right ClientIp + UserAgent forwarded.
	if up.lastBegin.GetClientIp() != "203.0.113.4" {
		t.Errorf("upstream ClientIp = %q, want 203.0.113.4 (XFF first hop)", up.lastBegin.GetClientIp())
	}
}

// Scenario: 2.3-UNIT-062 (negative)
// Invalid return_to → 400 BEFORE upstream call.
func TestOAuthInitiate_InvalidReturnTo(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate?return_to=https://attacker.com/", nil)
	rec := httptest.NewRecorder()
	h.Initiate("google")(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "400_oauth_invalid_return_to") {
		t.Errorf("body = %q, want 400_oauth_invalid_return_to", rec.Body.String())
	}
	// Upstream never called.
	if up.lastBegin != nil {
		t.Error("upstream called despite return_to rejection")
	}
}

// Scenario: 2.3-SEC-002 + BR-1.3
// Callback without he_oauth_state cookie → 400 BEFORE upstream.
func TestOAuthCallback_MissingCookie(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?code=c&state=s", nil)
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "400_oauth_state_invalid") {
		t.Errorf("body = %q, want 400_oauth_state_invalid", rec.Body.String())
	}
	if up.lastComplete != nil {
		t.Error("upstream CompleteOAuth called despite missing cookie")
	}
}

// Scenario: 2.3-SEC-003
// Callback with cookie value != query.state → 400 BEFORE upstream.
func TestOAuthCallback_CookieMismatch(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?code=c&state=different-state", nil)
	req.AddCookie(&http.Cookie{Name: OAuthStateCookieName, Value: "cookie-state"})
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if up.lastComplete != nil {
		t.Error("upstream CompleteOAuth called despite cookie mismatch")
	}
}

// Scenario: 2.3-UNIT-063
// Callback happy path → he_access + he_refresh cookies, 302 to return_to.
func TestOAuthCallback_HappyPath(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{
		completeResp: &authv1.CompleteOAuthResponse{
			UserId:       "user-uuid",
			AccessToken:  "access.jwt.sig",
			RefreshToken: "refresh.jwt.sig",
			IsNewUser:    true,
			ReturnTo:     "https://console.he-api.com/en/dashboard",
			LinkOutcome:  authv1.LinkOutcome_LINK_OUTCOME_NEW_USER,
		},
	}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?code=code&state=state-abc", nil)
	req.AddCookie(&http.Cookie{Name: OAuthStateCookieName, Value: "state-abc"})
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://console.he-api.com/en/dashboard" {
		t.Errorf("Location = %q, want return_to passthrough", loc)
	}
	// he_access + he_refresh + cleared he_oauth_state cookies.
	gotAccess, gotRefresh, gotClearState := false, false, false
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case AccessCookieName:
			gotAccess = true
			if c.Value != "access.jwt.sig" {
				t.Errorf("access cookie value = %q", c.Value)
			}
		case RefreshCookieName:
			gotRefresh = true
		case OAuthStateCookieName:
			if c.MaxAge < 0 {
				gotClearState = true
			}
		}
	}
	if !gotAccess {
		t.Error("he_access cookie missing")
	}
	if !gotRefresh {
		t.Error("he_refresh cookie missing")
	}
	if !gotClearState {
		t.Error("he_oauth_state cookie not cleared (MaxAge<0 expected)")
	}
}

// Scenario: 2.3-UNIT-064
// Callback with provider 5xx → 302 to /{locale}/signin?oauth_error=provider_error.
func TestOAuthCallback_UpstreamProviderError(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{
		completeErr: connect.NewError(connect.CodeUnavailable, &mockErr{msg: "502_oauth_provider_error"}),
	}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?code=c&state=s", nil)
	req.AddCookie(&http.Cookie{Name: OAuthStateCookieName, Value: "s"})
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302 to signin", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "signin") || !strings.Contains(loc, "oauth_error=provider_error") {
		t.Errorf("Location = %q, want signin with oauth_error", loc)
	}
}

// Scenario: 2.3-UI / provider cancel
// Provider returns ?error=access_denied → 302 to signin with oauth_error=user_denied.
func TestOAuthCallback_UserDenied(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?error=access_denied", nil)
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "oauth_error=user_denied") {
		t.Errorf("Location = %q, want oauth_error=user_denied", rec.Header().Get("Location"))
	}
}

// Scenario: 2.3 / 2FA hook end-to-end
// CompleteOAuth Requires_2Fa=true → 302 to /{locale}/2fa-challenge.
func TestOAuthCallback_Requires2FA(t *testing.T) {
	t.Parallel()
	up := &fakeOAuthUpstream{
		completeResp: &authv1.CompleteOAuthResponse{
			UserId:      "uid",
			Requires_2Fa: true,
			ReturnTo:    "https://console.he-api.com/en/dashboard",
		},
	}
	h := &OAuthHandler{Upstream: up, Env: EnvProduction}

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/callback?code=c&state=s", nil)
	req.AddCookie(&http.Cookie{Name: OAuthStateCookieName, Value: "s"})
	rec := httptest.NewRecorder()
	h.Callback("google")(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("code = %d, want 302", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "/2fa-challenge") {
		t.Errorf("Location = %q, want /2fa-challenge", rec.Header().Get("Location"))
	}
	// No he_access or he_refresh on 2FA hook path.
	for _, c := range rec.Result().Cookies() {
		if c.Name == AccessCookieName || c.Name == RefreshCookieName {
			if c.Value != "" {
				t.Errorf("cookie %s set on 2FA path: %q", c.Name, c.Value)
			}
		}
	}
}

// mockErr lets us construct an error with a chosen Message().
type mockErr struct{ msg string }

func (m *mockErr) Error() string { return m.msg }
