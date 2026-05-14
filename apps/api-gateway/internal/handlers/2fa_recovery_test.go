package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// Scenario: 2.4-INT-013 — UseRecoveryCode happy path: swaps cookies, returns
// remaining count + low flag.
func TestUseRecoveryCode_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		useRecoveryResp: &authv1.UseRecoveryCodeResponse{
			AccessToken:                  "access-tok",
			RefreshToken:                 "refresh-tok",
			AccessTokenExpiresInSeconds:  900,
			RefreshTokenExpiresInSeconds: 2592000,
			Aal:                          2,
			RecoveryCodesRemaining:       2,
			RecoveryCodesLow:             true,
			ReturnTo:                     "/dashboard",
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/recovery-codes/use",
		strings.NewReader(`{"code":"ABCD-EFG-HJ2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_mfa", Value: "mfa-tok"})
	rr := httptest.NewRecorder()
	proxy.UseRecoveryCode(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastUseRecoveryReq.GetCode() != "ABCD-EFG-HJ2" {
		t.Errorf("code passed: %q", fake.lastUseRecoveryReq.GetCode())
	}
	// Cookies swapped.
	gotAccess, gotMFACleared := false, false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "he_access" && c.Value == "access-tok" {
			gotAccess = true
		}
		if c.Name == "he_mfa" && c.MaxAge < 0 {
			gotMFACleared = true
		}
	}
	if !gotAccess {
		t.Errorf("he_access not set")
	}
	if !gotMFACleared {
		t.Errorf("he_mfa not cleared")
	}
	var body map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if body["recovery_codes_low"] != true {
		t.Errorf("low flag missing: %v", body["recovery_codes_low"])
	}
}

// Scenario: 2.4-INT-014 — Regenerate happy path: returns 10 fresh plaintext
// codes; auth-svc receives factor=TOTP + the user_id from JWT context.
func TestRegenerateRecoveryCodes_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		regenerateResp: &authv1.RegenerateRecoveryCodesResponse{
			RecoveryCodes: []string{"AAA1112222", "BBB1112222", "CCC1112222", "DDD1112222", "EEE1112222",
				"FFF1112222", "GGG1112222", "HHH1112222", "III1112222", "JJJ1112222"},
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	userID := "33333333-3333-3333-3333-333333333333"
	tok := signAccessToken(t, key, userID, 15*time.Minute)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.RegenerateRecoveryCodes))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/recovery-codes/regenerate",
		strings.NewReader(`{"factor":"totp","value":"123456"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastRegenerateReq.GetUserId() != userID {
		t.Errorf("user_id passed: %q", fake.lastRegenerateReq.GetUserId())
	}
	if fake.lastRegenerateReq.GetFactor() != authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP {
		t.Errorf("factor passed: %v", fake.lastRegenerateReq.GetFactor())
	}
	var body map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&body)
	codes, _ := body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Errorf("recovery_codes len=%d want 10", len(codes))
	}
}

// Scenario: 2.4-INT-015 — invalid factor string → 400_invalid_factor (gateway-side parse).
func TestRegenerateRecoveryCodes_InvalidFactor(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	tok := signAccessToken(t, key, "33333333-3333-3333-3333-333333333333", 15*time.Minute)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.RegenerateRecoveryCodes))

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/recovery-codes/regenerate",
		strings.NewReader(`{"factor":"webauthn","value":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastRegenerateReq != nil {
		t.Errorf("auth-svc invoked despite invalid factor")
	}
}
