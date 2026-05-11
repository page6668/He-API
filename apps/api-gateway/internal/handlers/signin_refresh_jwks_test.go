package handlers_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// --- Signin tests --------------------------------------------------------

// Happy path: 200 + {status:"ok"} + Set-Cookie pair with the canonical
// BR-3.7 attribute matrix for production.
func TestSignin_HappyPath_ProductionCookies(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.loginResp = &authv1.LoginUserResponse{
		Status:                       authv1.LoginStatus_LOGIN_STATUS_OK,
		AccessToken:                  "access.token.value",
		RefreshToken:                 "refresh.token.value",
		AccessTokenExpiresInSeconds:  900,
		RefreshTokenExpiresInSeconds: 2592000,
	}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	rr := postJSON(t, p.Signin, map[string]string{
		"email":    "user@example.com",
		"password": "correct horse battery staple",
	}, map[string]string{"X-Forwarded-For": "1.2.3.4"})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeEnvelope(t, rr)
	if got["status"] != "ok" {
		t.Errorf("body.status = %v, want ok", got["status"])
	}
	// Tokens MUST NOT leak in the body.
	for _, banned := range []string{"access_token", "refresh_token", "AccessToken", "RefreshToken"} {
		if _, ok := got[banned]; ok {
			t.Errorf("body unexpectedly carries token field %q", banned)
		}
	}

	cookies := rr.Result().Cookies()
	access, refresh := cookiesByName(cookies)
	if access == nil {
		t.Fatalf("he_access cookie missing")
	}
	if refresh == nil {
		t.Fatalf("he_refresh cookie missing")
	}
	// Access cookie attribute matrix.
	if access.Value != "access.token.value" {
		t.Errorf("access cookie value mismatch")
	}
	if !access.HttpOnly {
		t.Errorf("access cookie not HttpOnly")
	}
	if !access.Secure {
		t.Errorf("access cookie not Secure in production")
	}
	if access.SameSite != http.SameSiteLaxMode {
		t.Errorf("access SameSite = %v, want Lax", access.SameSite)
	}
	if access.Path != "/" {
		t.Errorf("access Path = %q, want /", access.Path)
	}
	// Go's net/http normalizes the leading dot off RFC-6265-§4.1.2.3
	// (Domain=he-api.com is equivalent to Domain=.he-api.com after parsing).
	// The header WE EMIT carries the dot; what the test re-parses doesn't.
	if access.Domain != "he-api.com" {
		t.Errorf("access Domain = %q, want he-api.com (production, post net/http parse normalization)", access.Domain)
	}
	if access.MaxAge != 900 {
		t.Errorf("access MaxAge = %d, want 900", access.MaxAge)
	}
	// Refresh cookie attribute matrix.
	if refresh.Value != "refresh.token.value" {
		t.Errorf("refresh cookie value mismatch")
	}
	if !refresh.HttpOnly {
		t.Errorf("refresh cookie not HttpOnly")
	}
	if !refresh.Secure {
		t.Errorf("refresh cookie not Secure in production")
	}
	if refresh.SameSite != http.SameSiteStrictMode {
		t.Errorf("refresh SameSite = %v, want Strict", refresh.SameSite)
	}
	if refresh.Path != "/v1/auth/refresh" {
		t.Errorf("refresh Path = %q, want /v1/auth/refresh", refresh.Path)
	}
	if refresh.Domain != "he-api.com" {
		t.Errorf("refresh Domain = %q, want he-api.com", refresh.Domain)
	}
	if refresh.MaxAge != 2592000 {
		t.Errorf("refresh MaxAge = %d, want 2592000", refresh.MaxAge)
	}
}

// Development env: Secure=false; Domain="" (host-only).
func TestSignin_DevelopmentCookies(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.loginResp = &authv1.LoginUserResponse{
		Status:                       authv1.LoginStatus_LOGIN_STATUS_OK,
		AccessToken:                  "a",
		RefreshToken:                 "r",
		AccessTokenExpiresInSeconds:  900,
		RefreshTokenExpiresInSeconds: 2592000,
	}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvDevelopment)

	rr := postJSON(t, p.Signin, map[string]string{"email": "u@e.com", "password": "p"}, nil)
	access, refresh := cookiesByName(rr.Result().Cookies())
	if access.Secure || refresh.Secure {
		t.Errorf("Secure must be false in development (access=%v, refresh=%v)", access.Secure, refresh.Secure)
	}
	if access.Domain != "" || refresh.Domain != "" {
		t.Errorf("Domain must be empty in development (access=%q, refresh=%q)", access.Domain, refresh.Domain)
	}
}

// Staging env: Secure=true, Domain=.staging.he-api.com.
func TestSignin_StagingCookies(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.loginResp = &authv1.LoginUserResponse{
		Status: authv1.LoginStatus_LOGIN_STATUS_OK, AccessToken: "a", RefreshToken: "r",
		AccessTokenExpiresInSeconds: 900, RefreshTokenExpiresInSeconds: 2592000,
	}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvStaging)
	rr := postJSON(t, p.Signin, map[string]string{"email": "u@e.com", "password": "p"}, nil)
	access, refresh := cookiesByName(rr.Result().Cookies())
	if !access.Secure || !refresh.Secure {
		t.Errorf("Secure must be true in staging")
	}
	if access.Domain != "staging.he-api.com" || refresh.Domain != "staging.he-api.com" {
		t.Errorf("staging Domain wrong: access=%q refresh=%q", access.Domain, refresh.Domain)
	}
}

// 401 from auth-svc → HTTP 401, NO cookies set.
func TestSignin_401NoCookiesSet(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.loginErr = connect.NewError(connect.CodeUnauthenticated, errors.New("401_invalid_credentials"))
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	rr := postJSON(t, p.Signin, map[string]string{"email": "u@e.com", "password": "bad"}, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == handlers.AccessCookieName || c.Name == handlers.RefreshCookieName {
			t.Errorf("cookie %q was set on 401 response", c.Name)
		}
	}
}

// 423 account locked → HTTP 423 + Retry-After header passthrough, no cookies.
func TestSignin_423AccountLockedPassthrough(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	connectErr := connect.NewError(connect.CodeResourceExhausted, errors.New("423_account_locked"))
	connectErr.Meta().Set("Retry-After", "3600")
	fake.loginErr = connectErr
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	rr := postJSON(t, p.Signin, map[string]string{"email": "u@e.com", "password": "p"}, nil)
	if rr.Code != http.StatusLocked {
		t.Fatalf("status = %d, want 423", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "3600" {
		t.Errorf("Retry-After header missing or wrong")
	}
}

// 2FA hook: REQUIRES_2FA pass-through, NO tokens set as cookies.
func TestSignin_2FARequiredPassthroughNoCookies(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.loginResp = &authv1.LoginUserResponse{
		Status:    authv1.LoginStatus_LOGIN_STATUS_REQUIRES_2FA,
		MfaToken:  "mfa.intermediate.token",
	}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)
	rr := postJSON(t, p.Signin, map[string]string{"email": "u@e.com", "password": "p"}, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["status"] != "requires_2fa" {
		t.Errorf("body.status = %v, want requires_2fa", got["status"])
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == handlers.AccessCookieName || c.Name == handlers.RefreshCookieName {
			t.Errorf("cookie %q was set on 2FA-required response", c.Name)
		}
	}
}

// --- Refresh tests -------------------------------------------------------

// Refresh happy: reads he_refresh cookie → calls upstream → returns new
// pair as cookies.
func TestRefresh_HappyRotation(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.refreshResp = &authv1.RefreshTokenResponse{
		AccessToken:                  "new.access",
		RefreshToken:                 "new.refresh",
		AccessTokenExpiresInSeconds:  900,
		RefreshTokenExpiresInSeconds: 2592000,
	}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: handlers.RefreshCookieName, Value: "presented.refresh"})
	rr := httptest.NewRecorder()
	p.Refresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastRefreshReq.GetRefreshToken() != "presented.refresh" {
		t.Errorf("upstream refresh token = %q, want presented.refresh", fake.lastRefreshReq.GetRefreshToken())
	}
	access, refresh := cookiesByName(rr.Result().Cookies())
	if access.Value != "new.access" || refresh.Value != "new.refresh" {
		t.Errorf("rotated cookies wrong: access=%q refresh=%q", access.Value, refresh.Value)
	}
}

// Missing he_refresh cookie → 401, no upstream call.
func TestRefresh_MissingCookieReturns401(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", strings.NewReader(""))
	rr := httptest.NewRecorder()
	p.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if fake.lastRefreshReq != nil {
		t.Errorf("upstream called without refresh cookie")
	}
}

// Upstream 401 (reuse / family revoked) → gateway clears BOTH cookies
// and returns 401. Client must re-sign-in.
func TestRefresh_UpstreamFailureClearsCookies(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	fake.refreshErr = connect.NewError(connect.CodeUnauthenticated, errors.New("401_invalid_credentials"))
	p := handlers.NewAuthProxyWithEnv(fake, handlers.EnvProduction)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", strings.NewReader(""))
	req.AddCookie(&http.Cookie{Name: handlers.RefreshCookieName, Value: "stale"})
	rr := httptest.NewRecorder()
	p.Refresh(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	// Both cookies MUST be cleared (MaxAge<0 → browser deletes).
	var clearedAccess, clearedRefresh bool
	for _, c := range rr.Result().Cookies() {
		switch c.Name {
		case handlers.AccessCookieName:
			if c.MaxAge < 0 {
				clearedAccess = true
			}
		case handlers.RefreshCookieName:
			if c.MaxAge < 0 {
				clearedRefresh = true
			}
		}
	}
	if !clearedAccess {
		t.Errorf("access cookie not cleared on refresh failure")
	}
	if !clearedRefresh {
		t.Errorf("refresh cookie not cleared on refresh failure")
	}
}

// --- JWKS tests ----------------------------------------------------------

// Happy: serves the supplied JWKS bytes with the right Content-Type
// + Cache-Control.
func TestJWKS_ServesBytesWithHeaders(t *testing.T) {
	t.Parallel()
	const body = `{"keys":[{"kty":"RSA","alg":"RS256"}]}`
	h := handlers.NewJWKSHandler([]byte(body))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Body.String() != body {
		t.Errorf("body = %q, want %q", rr.Body.String(), body)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("Content-Type = %q, want application/jwk-set+json", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", got)
	}
}

// Empty JWKS body → 503 (cmd/server bug — failed to load).
func TestJWKS_EmptyBodyReturns503(t *testing.T) {
	t.Parallel()
	h := handlers.NewJWKSHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// --- helpers --------------------------------------------------------------

func cookiesByName(cookies []*http.Cookie) (access, refresh *http.Cookie) {
	for _, c := range cookies {
		switch c.Name {
		case handlers.AccessCookieName:
			access = c
		case handlers.RefreshCookieName:
			refresh = c
		}
	}
	return
}
