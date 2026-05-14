package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// Scenario: 2.4-INT-016 — Disable happy path through JWT middleware.
func TestDisableTOTP_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		disableResp: &authv1.DisableTOTPResponse{Ok: true},
	}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	userID := "33333333-3333-3333-3333-333333333333"
	tok := signAccessToken(t, key, userID, 15*time.Minute)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.DisableTOTP))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/disable",
		strings.NewReader(`{"factor":"totp","value":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastDisableReq.GetUserId() != userID {
		t.Errorf("user_id passed: %q", fake.lastDisableReq.GetUserId())
	}
	if fake.lastDisableReq.GetFactor() != authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP {
		t.Errorf("factor: %v", fake.lastDisableReq.GetFactor())
	}
	if fake.lastDisableReq.GetValue() != "123456" {
		t.Errorf("value: %q", fake.lastDisableReq.GetValue())
	}
}

// Scenario: 2.4-INT-017 — missing he_access cookie → 401 (gateway middleware
// short-circuits before auth-svc).
func TestDisableTOTP_MissingJWTReturns401(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	proxy := handlers.NewAuthProxy(fake)
	verifier, _ := newJWTMiddleware(t)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.DisableTOTP))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/disable",
		strings.NewReader(`{"factor":"totp","value":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rr.Code)
	}
	if fake.lastDisableReq != nil {
		t.Errorf("auth-svc invoked despite missing cookie")
	}
}
