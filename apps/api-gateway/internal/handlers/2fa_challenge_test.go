package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"errors"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// Scenario: 2.4-INT-007 — Signin REQUIRES_2FA path sets he_mfa cookie
// with Path=/v1/auth/2fa + HttpOnly, and emits body {status:"requires_2fa"}.
func TestSignin_RequiresTwoFASetsHeMFACookie(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		loginResp: &authv1.LoginUserResponse{
			Status:   authv1.LoginStatus_LOGIN_STATUS_REQUIRES_2FA,
			MfaToken: "eyJabc.mfa.def",
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signin", strings.NewReader(`{"email":"u@example.com","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	proxy.Signin(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["status"] != "requires_2fa" {
		t.Errorf("status=%v want requires_2fa", body["status"])
	}
	// Set-Cookie he_mfa header MUST be present with the mfa_token value.
	cookies := rr.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "he_mfa" {
			found = true
			if c.Value != "eyJabc.mfa.def" {
				t.Errorf("mfa cookie value mismatch: %q", c.Value)
			}
			if c.Path != "/v1/auth/2fa" {
				t.Errorf("mfa cookie Path=%q want /v1/auth/2fa", c.Path)
			}
			if !c.HttpOnly {
				t.Errorf("mfa cookie not HttpOnly")
			}
		}
	}
	if !found {
		t.Fatalf("he_mfa cookie not set")
	}
	// he_access / he_refresh MUST NOT be set on the requires_2fa branch.
	for _, c := range cookies {
		if c.Name == "he_access" || c.Name == "he_refresh" {
			t.Errorf("%s cookie should NOT be set on requires_2fa", c.Name)
		}
	}
}

// Scenario: 2.4-INT-008 — ChallengeTOTP happy path swaps cookies + returns 200.
func TestChallengeTOTP_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		challengeResp: &authv1.ChallengeTOTPResponse{
			AccessToken:                  "access-tok",
			RefreshToken:                 "refresh-tok",
			AccessTokenExpiresInSeconds:  900,
			RefreshTokenExpiresInSeconds: 2592000,
			Aal:                          2,
			ReturnTo:                     "/dashboard",
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/challenge",
		strings.NewReader(`{"code":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_mfa", Value: "mfa-tok"})
	rr := httptest.NewRecorder()
	proxy.ChallengeTOTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastChallengeReq.GetMfaToken() != "mfa-tok" {
		t.Errorf("auth-svc not given the mfa_token from cookie")
	}
	if fake.lastChallengeReq.GetCode() != "123456" {
		t.Errorf("code passed: %q", fake.lastChallengeReq.GetCode())
	}

	cookies := rr.Result().Cookies()
	gotAccess, gotRefresh, gotMFACleared := false, false, false
	for _, c := range cookies {
		switch c.Name {
		case "he_access":
			gotAccess = c.Value == "access-tok"
		case "he_refresh":
			gotRefresh = c.Value == "refresh-tok"
		case "he_mfa":
			if c.MaxAge < 0 {
				gotMFACleared = true
			}
		}
	}
	if !gotAccess {
		t.Errorf("he_access not set")
	}
	if !gotRefresh {
		t.Errorf("he_refresh not set")
	}
	if !gotMFACleared {
		t.Errorf("he_mfa not cleared on success")
	}
	var body map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["return_to"] != "/dashboard" {
		t.Errorf("return_to: %v", body["return_to"])
	}
}

// Scenario: 2.4-INT-010 — missing he_mfa cookie → 401_mfa_token_invalid.
func TestChallengeTOTP_MissingCookieReturns401(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/challenge", strings.NewReader(`{"code":"123456"}`))
	rr := httptest.NewRecorder()
	proxy.ChallengeTOTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	if fake.lastChallengeReq != nil {
		t.Errorf("auth-svc invoked despite missing cookie")
	}
}

// Scenario: 2.4-INT-009 — account-locked response clears he_mfa.
func TestChallengeTOTP_LockedClearsMFACookie(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		challengeErr: connect.NewError(connect.CodeResourceExhausted, errors.New("423_account_locked")),
	}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/challenge", strings.NewReader(`{"code":"000000"}`))
	req.AddCookie(&http.Cookie{Name: "he_mfa", Value: "mfa-tok"})
	rr := httptest.NewRecorder()
	proxy.ChallengeTOTP(rr, req)
	if rr.Code != http.StatusLocked {
		t.Fatalf("status=%d want 423 (account locked); body=%s", rr.Code, rr.Body.String())
	}
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "he_mfa" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("he_mfa should be cleared on account_locked")
	}
}

// Scenario: 2.4-INT-011 — wrong code (401_invalid_totp_code) keeps he_mfa
// intact so the user can retry within the 5-min TTL.
func TestChallengeTOTP_WrongCodeKeepsMFACookie(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		challengeErr: connect.NewError(connect.CodeUnauthenticated, errors.New("401_invalid_totp_code")),
	}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/challenge", strings.NewReader(`{"code":"999999"}`))
	req.AddCookie(&http.Cookie{Name: "he_mfa", Value: "mfa-tok"})
	rr := httptest.NewRecorder()
	proxy.ChallengeTOTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	cleared := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "he_mfa" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if cleared {
		t.Errorf("he_mfa should NOT be cleared on wrong code (retry within TTL)")
	}
}
