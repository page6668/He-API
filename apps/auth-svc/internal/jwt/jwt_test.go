package jwt_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/jwt"
)

// Test-key generation is expensive (~1-2s for RSA 4096). Cache one keypair
// across the test process via sync.Once.
var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func getTestRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 4096)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		testKey = k
	})
	return testKey
}

func encodePrivatePEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func encodePublicPEM(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func newSignerVerifier(t *testing.T) (*jwt.Signer, *jwt.Verifier) {
	t.Helper()
	key := getTestRSAKey(t)
	signer, err := jwt.NewSigner(encodePrivatePEM(t, key))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	verifier, err := jwt.NewVerifier(encodePublicPEM(t, &key.PublicKey))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return signer, verifier
}

// --- tests ---------------------------------------------------------------

// Scenario: 2.2-UNIT-110
// SignAccessToken produces a JWT whose `alg` header is RS256.
func TestSignAccessToken_AlgIsRS256(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	userID := uuid.New()
	tok, err := signer.SignAccessToken(userID, time.Now())
	if err != nil {
		t.Fatalf("SignAccessToken: %v", err)
	}
	hdr := decodeJWTHeader(t, tok)
	if got := hdr["alg"]; got != "RS256" {
		t.Errorf("alg = %v, want RS256", got)
	}
}

// Scenario: 2.2-UNIT-114
// RSA key size invariant: NewSigner refuses < MinRSAKeyBits.
func TestNewSigner_RejectsKeySmallerThan4096(t *testing.T) {
	t.Parallel()
	small, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey 2048: %v", err)
	}
	_, err = jwt.NewSigner(encodePrivatePEM(t, small))
	if !errors.Is(err, jwt.ErrKeyTooSmall) {
		t.Fatalf("NewSigner(2048) = %v, want ErrKeyTooSmall", err)
	}
}

// 4096-bit key is accepted (boundary on the allowed side).
func TestNewSigner_AcceptsKeyExactly4096(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	if signer == nil {
		t.Fatalf("nil signer for 4096-bit key")
	}
}

// Scenario: 2.2-UNIT-111
// Issued access-token claims contain EXACTLY {sub, iat, exp, jti, aud}.
// NO email, NO locale, NO role, NO scope. BR-3.6 minimization.
func TestSignAccessToken_ClaimsMinimization(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	userID := uuid.New()
	tok, err := signer.SignAccessToken(userID, time.Now())
	if err != nil {
		t.Fatalf("SignAccessToken: %v", err)
	}
	payload := decodeJWTPayload(t, tok)
	wantKeys := map[string]bool{"sub": true, "iat": true, "exp": true, "jti": true, "aud": true}
	for k := range payload {
		if !wantKeys[k] {
			t.Errorf("unexpected claim %q in access token (BR-3.6 minimization violated)", k)
		}
	}
	for k := range wantKeys {
		if _, ok := payload[k]; !ok {
			t.Errorf("missing required claim %q", k)
		}
	}
	for _, banned := range []string{"email", "locale", "role", "scope", "user_email", "user_role"} {
		if _, ok := payload[banned]; ok {
			t.Errorf("access token carries banned claim %q", banned)
		}
	}
}

// Scenario: 2.3-UNIT-WasLocked (BR-3.10)
// Default access tokens (password-login path) MUST NOT carry was_locked.
// Lock-bypass access tokens (OAuth path with outcome.WasLocked=true) MUST
// carry was_locked=true; wasLocked=false on the bypass variant drops the
// claim entirely (omitempty).
func TestSignAccessTokenWithLockBypass_EmbedsClaim(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	userID := uuid.New()

	// Default path — no was_locked.
	defaultTok, err := signer.SignAccessToken(userID, time.Now())
	if err != nil {
		t.Fatalf("SignAccessToken: %v", err)
	}
	if _, ok := decodeJWTPayload(t, defaultTok)["was_locked"]; ok {
		t.Errorf("default access token carries was_locked claim; want omitted (BR-3.6 minimization)")
	}

	// Bypass variant with wasLocked=false also omits the claim.
	noBypassTok, err := signer.SignAccessTokenWithLockBypass(userID, time.Now(), false)
	if err != nil {
		t.Fatalf("SignAccessTokenWithLockBypass(false): %v", err)
	}
	if _, ok := decodeJWTPayload(t, noBypassTok)["was_locked"]; ok {
		t.Errorf("wasLocked=false token carries was_locked claim; want omitted")
	}

	// Bypass variant with wasLocked=true MUST carry the claim.
	bypassTok, err := signer.SignAccessTokenWithLockBypass(userID, time.Now(), true)
	if err != nil {
		t.Fatalf("SignAccessTokenWithLockBypass(true): %v", err)
	}
	payload := decodeJWTPayload(t, bypassTok)
	if got := payload["was_locked"]; got != true {
		t.Errorf("was_locked = %v, want true", got)
	}
}

// Refresh tokens additionally carry `fam` (family_id) per BR-3.9.
func TestSignRefreshToken_CarriesFamilyClaim(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	userID := uuid.New()
	family := uuid.New()
	tok, err := signer.SignRefreshToken(userID, family, time.Now())
	if err != nil {
		t.Fatalf("SignRefreshToken: %v", err)
	}
	payload := decodeJWTPayload(t, tok)
	if got, want := payload["fam"], family.String(); got != want {
		t.Errorf("fam = %v, want %s", got, want)
	}
	// Access-only claims (sub/iat/exp/jti/aud) MUST still be present.
	for _, k := range []string{"sub", "iat", "exp", "jti", "aud"} {
		if _, ok := payload[k]; !ok {
			t.Errorf("refresh token missing required claim %q", k)
		}
	}
}

// Verify happy path returns parsed Claims with the right sub.
func TestVerify_HappyPathReturnsClaims(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	userID := uuid.New()
	tok, _ := signer.SignAccessToken(userID, time.Now())
	claims, err := verifier.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != userID.String() {
		t.Errorf("Subject = %s, want %s", claims.Subject, userID.String())
	}
	if claims.Audience != jwt.Audience {
		t.Errorf("Audience = %s, want %s", claims.Audience, jwt.Audience)
	}
}

// Scenario: 2.2-UNIT-115
// Algorithm-confusion defense: a HS256-signed token (with attacker-chosen
// "key" = our PUBLIC key bytes — the classic CVE) MUST be rejected.
func TestVerify_RejectsHS256AlgorithmConfusion(t *testing.T) {
	t.Parallel()
	_, verifier := newSignerVerifier(t)
	// Build a HS256 token where the "secret" is the verifier's public key
	// PEM. Classic CVE-2015-9235 attack — we MUST reject this regardless
	// of how the signature verifies.
	publicKeyPEM := encodePublicPEM(t, &getTestRSAKey(t).PublicKey)
	claims := gojwt.MapClaims{
		"sub": uuid.New().String(),
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(15 * time.Minute).Unix(),
		"jti": uuid.New().String(),
		"aud": jwt.Audience,
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(publicKeyPEM)
	if err != nil {
		t.Fatalf("SignedString HS256: %v", err)
	}

	_, err = verifier.Verify(signed)
	if err == nil {
		t.Fatalf("HS256-signed token VERIFIED — algorithm-confusion defense broken")
	}
	if !errors.Is(err, jwt.ErrInvalidAlgorithm) && !strings.Contains(err.Error(), "alg") {
		t.Errorf("Verify(HS256) error = %v, want ErrInvalidAlgorithm or similar", err)
	}
}

// Expired token returns ErrExpired.
func TestVerify_ExpiredTokenReturnsErrExpired(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	userID := uuid.New()
	// Sign with `now` deep in the past so the 15-min TTL has elapsed.
	pastNow := time.Now().Add(-2 * jwt.AccessTokenTTL)
	tok, _ := signer.SignAccessToken(userID, pastNow)

	_, err := verifier.Verify(tok)
	if !errors.Is(err, jwt.ErrExpired) {
		t.Fatalf("Verify(expired) = %v, want ErrExpired", err)
	}
}

// Tampered token (signature mismatch) returns ErrInvalidSignature.
func TestVerify_TamperedTokenReturnsErrInvalidSignature(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	userID := uuid.New()
	tok, _ := signer.SignAccessToken(userID, time.Now())
	// Flip a byte in the payload section to corrupt the signature.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT must have 3 parts, got %d", len(parts))
	}
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]
	_, err := verifier.Verify(tampered)
	if err == nil {
		t.Fatalf("tampered token VERIFIED")
	}
}

// JWKS document carries one RSA public key with the right shape.
func TestJWKS_RoundTripContainsCanonicalRSAKey(t *testing.T) {
	t.Parallel()
	_, verifier := newSignerVerifier(t)
	raw, err := verifier.JWKS()
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	var doc struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("JWKS unmarshal: %v\n%s", err, raw)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("JWKS keys = %d, want 1", len(doc.Keys))
	}
	k := doc.Keys[0]
	if k["kty"] != "RSA" {
		t.Errorf("kty = %q, want RSA", k["kty"])
	}
	if k["alg"] != "RS256" {
		t.Errorf("alg = %q, want RS256", k["alg"])
	}
	if k["use"] != "sig" {
		t.Errorf("use = %q, want sig", k["use"])
	}
	if k["kid"] == "" || k["kid"] != verifier.KeyID() {
		t.Errorf("kid = %q, want %q", k["kid"], verifier.KeyID())
	}
	if k["n"] == "" {
		t.Errorf("missing modulus `n`")
	}
	if k["e"] == "" {
		t.Errorf("missing exponent `e`")
	}
}

// Signer + Verifier built from the same key share a kid.
func TestSignerVerifier_KeyIDMatches(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	if signer.KeyID() != verifier.KeyID() {
		t.Fatalf("kid mismatch: signer=%s verifier=%s", signer.KeyID(), verifier.KeyID())
	}
}

// Issued token's `kid` header matches the JWKS kid (so api-gateway can
// look up the right key in a multi-key future).
func TestSignAccessToken_HeaderKidMatchesJWKS(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	tok, _ := signer.SignAccessToken(uuid.New(), time.Now())
	hdr := decodeJWTHeader(t, tok)
	if hdr["kid"] != verifier.KeyID() {
		t.Errorf("token kid = %v, want %s", hdr["kid"], verifier.KeyID())
	}
}

// --- helpers --------------------------------------------------------------

func decodeJWTHeader(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT must have 3 parts; got %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	return hdr
}

func decodeJWTPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT must have 3 parts; got %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload
}
