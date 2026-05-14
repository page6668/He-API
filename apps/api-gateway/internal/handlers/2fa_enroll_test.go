package handlers_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// Test-only RSA keypair (cached for whole test binary; 4096-bit gen is ~1s).
var (
	testKeyOnce sync.Once
	testRSAKey  *rsa.PrivateKey
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			t.Fatalf("gen rsa: %v", err)
		}
		testRSAKey = k
	})
	return testRSAKey
}

// signAccessToken builds a he_access cookie value that the middleware will
// verify against testRSAKey.
func signAccessToken(t *testing.T, key *rsa.PrivateKey, sub string, ttl time.Duration) string {
	t.Helper()
	claims := gojwt.MapClaims{
		"sub": sub,
		"aud": "he-api",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(ttl).Unix(),
		"jti": "test-jti-1",
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// signMFAToken returns a JWT carrying purpose='2fa_challenge' — the cross-
// token confusion attack: should be REJECTED when offered as he_access.
func signMFAToken(t *testing.T, key *rsa.PrivateKey, sub string) string {
	t.Helper()
	claims := gojwt.MapClaims{
		"sub":     sub,
		"aud":     "he-api",
		"iat":     time.Now().Unix(),
		"exp":     time.Now().Add(5 * time.Minute).Unix(),
		"jti":     "mfa-jti",
		"purpose": "2fa_challenge",
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign mfa: %v", err)
	}
	return signed
}

func newJWTMiddleware(t *testing.T) (*middleware.JWTVerifier, *rsa.PrivateKey) {
	t.Helper()
	key := testKey(t)
	return middleware.NewJWTVerifier(&key.PublicKey), key
}

// pemRSAPub returns a PEM-encoded RSA public key — used by parseRSAPublicPEM
// integration tests if needed.
func pemRSAPub(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// --- EnrollTOTPInit via middleware --------------------------------------

// Scenario: 2.4-INT-001 — happy path: valid he_access cookie → 200 with
// otpauth_uri + base64 qr + 10 recovery codes + iso expires_at.
func TestEnrollTOTPInit_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		enrollInitResp: &authv1.EnrollTOTPInitResponse{
			OtpauthUri:    "otpauth://totp/He-API:user?secret=ABC",
			QrCodePng:     []byte{0x89, 0x50, 0x4e, 0x47}, // PNG magic prefix
			RecoveryCodes: []string{"AAAAAAAAA2", "BBBBBBBBB2", "CCCCCCCCC2", "DDDDDDDDD2", "EEEEEEEEE2", "FFFFFFFFF2", "GGGGGGGGG2", "HHHHHHHHH2", "IIIIIIIII2", "JJJJJJJJJ2"},
			ExpiresAtUnix: time.Now().Add(10 * time.Minute).Unix(),
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	userID := "33333333-3333-3333-3333-333333333333"
	tok := signAccessToken(t, key, userID, 15*time.Minute)

	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.EnrollTOTPInit))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/enroll/init", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastEnrollInitReq == nil {
		t.Fatalf("auth-svc EnrollTOTPInit not invoked")
	}
	if fake.lastEnrollInitReq.GetUserId() != userID {
		t.Errorf("user_id passed: got %q want %q", fake.lastEnrollInitReq.GetUserId(), userID)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["otpauth_uri"] == "" {
		t.Errorf("otpauth_uri missing")
	}
	codes, _ := body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Errorf("recovery_codes len=%d want 10", len(codes))
	}
	if body["qr_code_png"] == "" {
		t.Errorf("qr_code_png missing")
	}
	// expires_at is ISO 8601.
	if _, err := time.Parse(time.RFC3339, body["expires_at"].(string)); err != nil {
		t.Errorf("expires_at not ISO: %v", body["expires_at"])
	}
}

// Scenario: 2.4-SEC-004 — no he_access cookie → 401.
func TestEnrollTOTPInit_MissingCookieReturns401(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	proxy := handlers.NewAuthProxy(fake)
	verifier, _ := newJWTMiddleware(t)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.EnrollTOTPInit))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/enroll/init", strings.NewReader("{}"))
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastEnrollInitReq != nil {
		t.Errorf("auth-svc invoked despite missing cookie")
	}
}

// Scenario: 2.4-UNIT-035 (gateway side) — mfa_token submitted as he_access
// → 401 with code=401_cross_token_rejected. Architect Q2 HARD constraint
// enforced at the gateway boundary too.
func TestEnrollTOTPInit_CrossTokenMFAAsAccessRejected(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	mfa := signMFAToken(t, key, "33333333-3333-3333-3333-333333333333")
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.EnrollTOTPInit))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/enroll/init", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "he_access", Value: mfa})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&body)
	if e, ok := body["error"].(map[string]any); ok {
		if e["code"] != "401_cross_token_rejected" {
			t.Errorf("error.code=%q want 401_cross_token_rejected", e["code"])
		}
	} else {
		t.Errorf("error envelope missing")
	}
	if fake.lastEnrollInitReq != nil {
		t.Errorf("auth-svc invoked despite cross-token rejection")
	}
}

// Scenario: 2.4-INT-002 — EnrollTOTPVerify happy path: body decoded + passed.
func TestEnrollTOTPVerify_GatewayHappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		enrollVerifyResp: &authv1.EnrollTOTPVerifyResponse{
			Ok:             true,
			EnrolledAtUnix: time.Now().Unix(),
		},
	}
	proxy := handlers.NewAuthProxy(fake)
	verifier, key := newJWTMiddleware(t)
	userID := "33333333-3333-3333-3333-333333333333"
	tok := signAccessToken(t, key, userID, 15*time.Minute)
	wrapped := verifier.RequireJWT(http.HandlerFunc(proxy.EnrollTOTPVerify))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/2fa/enroll/verify",
		strings.NewReader(`{"code":"123456","ack_recovery_codes_saved":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if fake.lastEnrollVerifyReq.GetCode() != "123456" {
		t.Errorf("code passed wrong: %q", fake.lastEnrollVerifyReq.GetCode())
	}
	if !fake.lastEnrollVerifyReq.GetAckRecoveryCodesSaved() {
		t.Errorf("ack flag not propagated")
	}
	if fake.lastEnrollVerifyReq.GetUserId() != userID {
		t.Errorf("user_id passed: %q", fake.lastEnrollVerifyReq.GetUserId())
	}
}

// Ensure pemRSAPub helper is referenced; future tests for parseRSAPublicPEM
// may use it.
var _ = pemRSAPub
var _ context.Context = context.Background()
