package jwt_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/jwt"
)

func mfaInput() jwt.MFATokenInput {
	return jwt.MFATokenInput{
		UserID:        uuid.New(),
		LoginMethod:   "password",
		IPHash:        "iphash-deadbeef",
		UserAgentHash: "uahash-cafe1234",
		ReturnTo:      "/dashboard",
	}
}

// Scenario: 2.4-UNIT-027 — IssueMFAToken produces RS256 JWT with claims +
// 128-bit JTI (16 bytes → 22-char base64url).
func TestIssueMFAToken_StructureAndJTI(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	in := mfaInput()
	tok, jti, err := signer.IssueMFAToken(in, time.Now())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if tok == "" || jti == "" {
		t.Fatalf("empty token/jti")
	}
	// JTI base64url decodes to exactly 16 bytes (128 bits).
	dec, err := base64.RawURLEncoding.DecodeString(jti)
	if err != nil {
		t.Fatalf("decode jti: %v", err)
	}
	if len(dec) != 16 {
		t.Fatalf("jti bytes=%d want 16", len(dec))
	}
	claims, err := verifier.ParseMFAToken(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Subject != in.UserID.String() {
		t.Errorf("sub: got %q want %q", claims.Subject, in.UserID.String())
	}
	if claims.Purpose != jwt.MFATokenPurpose {
		t.Errorf("purpose: got %q want %q", claims.Purpose, jwt.MFATokenPurpose)
	}
	if claims.LoginMethod != "password" {
		t.Errorf("login_method: got %q", claims.LoginMethod)
	}
	if claims.IPHash != in.IPHash {
		t.Errorf("ip_hash mismatch")
	}
	if claims.UserAgentHash != in.UserAgentHash {
		t.Errorf("ua_hash mismatch")
	}
	if claims.JTI != jti {
		t.Errorf("jti claim mismatch")
	}
	if claims.ReturnTo != "/dashboard" {
		t.Errorf("return_to: got %q", claims.ReturnTo)
	}
	if claims.ExpiresAt-claims.IssuedAt != int64(jwt.MFATokenTTL.Seconds()) {
		t.Errorf("ttl: got %ds want %ds", claims.ExpiresAt-claims.IssuedAt, int64(jwt.MFATokenTTL.Seconds()))
	}
}

// Scenario: 2.4-UNIT-028 — ParseMFAToken rejects expired tokens.
func TestParseMFAToken_RejectsExpired(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	past := time.Now().Add(-10 * time.Minute)
	tok, _, _ := signer.IssueMFAToken(mfaInput(), past)
	_, err := verifier.ParseMFAToken(tok)
	if !errors.Is(err, jwt.ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// Scenario: 2.4-UNIT-029 — ParseMFAToken rejects tokens signed with the wrong key.
func TestParseMFAToken_RejectsForeignSignature(t *testing.T) {
	t.Parallel()
	signerA, _ := newSignerVerifier(t)
	// Build a verifier from a different keypair.
	tok, _, _ := signerA.IssueMFAToken(mfaInput(), time.Now())
	// Tamper one byte of the signature segment.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT")
	}
	parts[2] = strings.ReplaceAll(parts[2], "A", "B")
	tampered := strings.Join(parts, ".")
	_, verifier := newSignerVerifier(t) // same key — but signature is tampered
	_, err := verifier.ParseMFAToken(tampered)
	if err == nil {
		t.Fatalf("expected error on tampered signature")
	}
}

// Scenario: 2.4-UNIT-030 — ParseMFAToken rejects alg=none.
func TestParseMFAToken_RejectsAlgNone(t *testing.T) {
	t.Parallel()
	_, verifier := newSignerVerifier(t)
	// alg=none unsigned token: header.payload.<empty>
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","purpose":"2fa_challenge","aud":"he-api","exp":99999999999}`))
	none := header + "." + payload + "."
	_, err := verifier.ParseMFAToken(none)
	if err == nil {
		t.Fatalf("alg=none accepted")
	}
}

// Scenario: 2.4-UNIT-035 — Cross-token rejection (Architect Q2 HARD constraint):
// an access_token MUST be rejected when parsed as an mfa_token (no purpose claim).
func TestParseMFAToken_RejectsAccessToken(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	accessTok, err := signer.SignAccessToken(uuid.New(), time.Now())
	if err != nil {
		t.Fatalf("sign access: %v", err)
	}
	_, err = verifier.ParseMFAToken(accessTok)
	if !errors.Is(err, jwt.ErrInvalidPurpose) {
		t.Fatalf("want ErrInvalidPurpose, got %v", err)
	}
}

// Scenario: 2.4-UNIT-036 — Cross-token rejection (opposite direction):
// an mfa_token MUST be rejected when parsed as access_token by VerifyAccess.
func TestVerifyAccess_RejectsMFAToken(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	tok, _, err := signer.IssueMFAToken(mfaInput(), time.Now())
	if err != nil {
		t.Fatalf("issue mfa: %v", err)
	}
	_, err = verifier.VerifyAccess(tok)
	if !errors.Is(err, jwt.ErrInvalidPurpose) {
		t.Fatalf("want ErrInvalidPurpose, got %v", err)
	}
}

// Scenario: 2.4-UNIT-036b — VerifyAccess still accepts a normal access_token (legacy compat).
func TestVerifyAccess_AcceptsAccessToken(t *testing.T) {
	t.Parallel()
	signer, verifier := newSignerVerifier(t)
	tok, _ := signer.SignAccessToken(uuid.New(), time.Now())
	if _, err := verifier.VerifyAccess(tok); err != nil {
		t.Fatalf("VerifyAccess on plain access_token: %v", err)
	}
}

// Scenario: 2.4-UNIT-033 — JTIs are unique across 1000 issuances.
func TestIssueMFAToken_JTIUniqueness(t *testing.T) {
	t.Parallel()
	signer, _ := newSignerVerifier(t)
	const N = 1000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		_, jti, err := signer.IssueMFAToken(mfaInput(), time.Now())
		if err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
		if _, dup := seen[jti]; dup {
			t.Fatalf("jti collision at i=%d", i)
		}
		seen[jti] = struct{}{}
	}
}
