package jwt

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// MFA-token tunables (BR-2.1, BR-2.3).
const (
	// MFATokenTTL is the hard 5-min lifetime of an mfa_token (BR-2.1).
	// Single-use after success (BR-2.2).
	MFATokenTTL = 5 * time.Minute

	// MFATokenPurpose is the JWT `purpose` claim value. Wright Round 1 Q2
	// (b): reuse main RS256 keypair but DISCRIMINATE by purpose. Access-
	// token-as-mfa-token and mfa-token-as-access-token MUST both reject.
	MFATokenPurpose = "2fa_challenge"
)

// Errors specific to MFA tokens.
var (
	// ErrInvalidPurpose surfaces when a JWT's purpose claim does not match
	// the expected value. Cross-token rejection per Wright Q2 hard constraint:
	//   - ParseAccessToken on an mfa_token → ErrInvalidPurpose (mfa_token
	//     carries purpose='2fa_challenge'; access tokens MUST be purpose-free
	//     or '').
	//   - ParseMFAToken on an access_token → ErrInvalidPurpose.
	ErrInvalidPurpose = errors.New("jwt: invalid purpose claim")
)

// MFATokenClaims is the JWT payload for the 5-min 2FA challenge token.
// Carries the bindings that ChallengeTOTP / UseRecoveryCode verify:
//   - sub: user_id (uuid)
//   - purpose: "2fa_challenge"
//   - login_method: "password" | "oauth_google" | "oauth_github"
//   - ip_hash / ua_hash: bound to the originating request (BR-2.3)
//   - jti: 128-bit random; Redis registry tracks single-use (BR-2.2)
//   - return_to: validated post-challenge redirect (BR-2.10)
//   - iat / exp: standard
//   - aud: same Audience constant as access tokens
type MFATokenClaims struct {
	Subject     string `json:"sub"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
	JTI         string `json:"jti"`
	Audience    string `json:"aud"`
	Purpose     string `json:"purpose"`
	LoginMethod string `json:"login_method"`
	IPHash      string `json:"ip_hash"`
	UserAgentHash string `json:"ua_hash"`
	ReturnTo    string `json:"return_to,omitempty"`
}

// GetExpirationTime implements gojwt.Claims.
func (c MFATokenClaims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.ExpiresAt, 0)), nil
}

// GetIssuedAt implements gojwt.Claims.
func (c MFATokenClaims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.IssuedAt, 0)), nil
}

// GetNotBefore implements gojwt.Claims.
func (c MFATokenClaims) GetNotBefore() (*gojwt.NumericDate, error) { return nil, nil }

// GetIssuer implements gojwt.Claims.
func (c MFATokenClaims) GetIssuer() (string, error) { return "", nil }

// GetSubject implements gojwt.Claims.
func (c MFATokenClaims) GetSubject() (string, error) { return c.Subject, nil }

// GetAudience implements gojwt.Claims.
func (c MFATokenClaims) GetAudience() (gojwt.ClaimStrings, error) {
	return gojwt.ClaimStrings{c.Audience}, nil
}

// MFATokenInput collects the bindings needed to issue an mfa_token. All
// fields required; ipHash/uaHash come from the Story 2.3 oauth.HashClientIP
// + oauth.HashUserAgent helpers (reused per BR-2.3).
type MFATokenInput struct {
	UserID        uuid.UUID
	LoginMethod   string
	IPHash        string
	UserAgentHash string
	ReturnTo      string
}

// IssueMFAToken signs a 5-min RS256 mfa_token. `now` is overridable for
// deterministic tests. Returns the signed token string AND the raw JTI so
// the handler can write the JTI registry entry to Redis before returning
// to the client (BR-5.4 single-use).
func (s *Signer) IssueMFAToken(in MFATokenInput, now time.Time) (token string, jti string, err error) {
	rawJTI, err := genJTI()
	if err != nil {
		return "", "", err
	}
	claims := MFATokenClaims{
		Subject:       in.UserID.String(),
		IssuedAt:      now.Unix(),
		ExpiresAt:     now.Add(MFATokenTTL).Unix(),
		JTI:           rawJTI,
		Audience:      Audience,
		Purpose:       MFATokenPurpose,
		LoginMethod:   in.LoginMethod,
		IPHash:        in.IPHash,
		UserAgentHash: in.UserAgentHash,
		ReturnTo:      in.ReturnTo,
	}
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.keyID
	signed, err := tok.SignedString(s.privateKey)
	if err != nil {
		return "", "", fmt.Errorf("jwt: sign mfa_token: %w", err)
	}
	return signed, rawJTI, nil
}

// ParseMFAToken validates an mfa_token JWT. Returns the parsed claims on
// success. Strictly enforces `purpose='2fa_challenge'` — an access_token
// (which omits purpose, or carries empty string) MUST fail (Wright Q2 hard
// constraint).
//
// Algorithm-confusion defense: alg=none and alg=HS256 are rejected at the
// keyfunc layer (same pattern as Verifier.Verify).
func (v *Verifier) ParseMFAToken(tokenString string) (*MFATokenClaims, error) {
	parsed, err := gojwt.ParseWithClaims(tokenString, &MFATokenClaims{}, func(t *gojwt.Token) (any, error) {
		if t.Method.Alg() != Algorithm {
			return nil, fmt.Errorf("%w: got %q", ErrInvalidAlgorithm, t.Method.Alg())
		}
		return v.publicKey, nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidAlgorithm):
			return nil, ErrInvalidAlgorithm
		case errors.Is(err, gojwt.ErrTokenExpired):
			return nil, ErrExpired
		case errors.Is(err, gojwt.ErrTokenSignatureInvalid):
			return nil, ErrInvalidSignature
		default:
			return nil, fmt.Errorf("%w: %v", ErrInvalidClaims, err)
		}
	}
	claims, ok := parsed.Claims.(*MFATokenClaims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidClaims
	}
	if claims.Audience != Audience {
		return nil, fmt.Errorf("%w: aud=%q", ErrInvalidClaims, claims.Audience)
	}
	// Hard cross-token rejection — the Wright Q2 (b) keypair-reuse design
	// requires every mfa_token validation path strictly check purpose.
	if claims.Purpose != MFATokenPurpose {
		return nil, ErrInvalidPurpose
	}
	return claims, nil
}

// VerifyAccess is a strict access-token validator that REJECTS any token
// carrying purpose='2fa_challenge' (Wright Q2 hard constraint — paranoid
// cross-token rejection). Use this in handlers that consume access_tokens
// and want to fail closed on mfa-token confusion.
//
// Existing callers using Verifier.Verify get the legacy behavior (no purpose
// check) — those paths inspect Claims and don't carry the purpose field.
// New callers should prefer VerifyAccess for defense-in-depth.
func (v *Verifier) VerifyAccess(tokenString string) (*Claims, error) {
	// First do the existing strict access-token parse (sig + alg + exp +
	// aud). Then sniff the raw payload for a `purpose` claim — if present
	// and non-empty, reject. We parse twice to avoid changing the shape of
	// Claims (which is shared across many callers); the second parse uses
	// a minimal struct with only the purpose field.
	claims, err := v.Verify(tokenString)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Purpose string `json:"purpose"`
	}
	if parsed, _ := gojwt.ParseWithClaims(tokenString, &struct {
		gojwt.RegisteredClaims
		Purpose string `json:"purpose"`
	}{}, func(t *gojwt.Token) (any, error) {
		return v.publicKey, nil
	}); parsed != nil && parsed.Claims != nil {
		if pc, ok := parsed.Claims.(*struct {
			gojwt.RegisteredClaims
			Purpose string `json:"purpose"`
		}); ok {
			probe.Purpose = pc.Purpose
		}
	}
	if probe.Purpose != "" {
		return nil, ErrInvalidPurpose
	}
	return claims, nil
}

// genJTI returns a 128-bit (16-byte) base64url JTI. SHA-256 collision-
// resistant; matches Story 2.2 refresh-token jti format.
func genJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("jwt: rand jti: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
