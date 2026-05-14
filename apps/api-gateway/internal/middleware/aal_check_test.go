package middleware_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// Cached 4096-bit RSA key — generation is the slow part.
var (
	aalTestKeyOnce sync.Once
	aalTestKey     *rsa.PrivateKey
)

func aalKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	aalTestKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			t.Fatalf("gen rsa: %v", err)
		}
		aalTestKey = k
	})
	return aalTestKey
}

// signWithAAL produces an access JWT with the supplied aal claim.
func signWithAAL(t *testing.T, key *rsa.PrivateKey, sub string, aal int32) string {
	t.Helper()
	c := gojwt.MapClaims{
		"sub": sub,
		"aud": "he-api",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"jti": "test-jti",
	}
	if aal > 0 {
		c["aal"] = aal
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, c)
	out, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

// Scenario: 2.4-UNIT-077 — aal=2 token passes the gate.
func TestRequireAAL_AAL2Allowed(t *testing.T) {
	t.Parallel()
	key := aalKey(t)
	verifier := middleware.NewJWTVerifier(&key.PublicKey)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	wrapped := verifier.RequireJWT(verifier.RequireAAL(2, inner))
	tok := signWithAAL(t, key, "33333333-3333-3333-3333-333333333333", 2)
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Errorf("inner handler not called on aal=2")
	}
}

// Scenario: 2.4-UNIT-078 — aal=1 token rejected with 403_aal2_required.
func TestRequireAAL_AAL1Rejected(t *testing.T) {
	t.Parallel()
	key := aalKey(t)
	verifier := middleware.NewJWTVerifier(&key.PublicKey)
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	wrapped := verifier.RequireJWT(verifier.RequireAAL(2, inner))
	tok := signWithAAL(t, key, "33333333-3333-3333-3333-333333333333", 1)
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
	if called {
		t.Errorf("inner should NOT be called on aal=1")
	}
	if !contains(rr.Body.String(), "403_aal2_required") {
		t.Errorf("body missing 403_aal2_required: %s", rr.Body.String())
	}
}

// Scenario: 2.4-UNIT-079 — no aal claim (legacy pre-2.4 token) defaults to
// AAL1 and is rejected by RequireAAL(2).
func TestRequireAAL_NoClaimDefaultsToAAL1(t *testing.T) {
	t.Parallel()
	key := aalKey(t)
	verifier := middleware.NewJWTVerifier(&key.PublicKey)
	wrapped := verifier.RequireJWT(verifier.RequireAAL(2, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	tok := signWithAAL(t, key, "33333333-3333-3333-3333-333333333333", 0) // omitted
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.AddCookie(&http.Cookie{Name: "he_access", Value: tok})
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rr.Code)
	}
}

// AALFromContext default-to-1 test.
func TestAALFromContext_Default(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := middleware.AALFromContext(ctx); got != 1 {
		t.Errorf("default AAL=%d want 1", got)
	}
	ctx = middleware.WithAAL(ctx, 2)
	if got := middleware.AALFromContext(ctx); got != 2 {
		t.Errorf("after WithAAL(2): %d", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
