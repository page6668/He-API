// Package jwt isolates RS256 access + refresh token signing and verification.
//
// Invariants (TS-CONS-002, BR-3.5, BR-3.6, BR-3.9):
//   - Algorithm: RS256 (RSA private key signs; public key verifies). HS256
//     and "none" are explicitly rejected (algorithm-confusion defense per
//     OWASP ASVS L2 V3.5.5).
//   - RSA key size: ≥ MinRSAKeyBits (4096). The Signer constructor refuses
//     anything smaller so a misconfigured Secret can never weaken the
//     signature in production.
//   - Claim set: exactly {sub, iat, exp, jti, aud} per BR-3.6. No email,
//     no locale, no role — those are looked up by api-gateway from PG /
//     cache via sub (the user_id).
//   - Refresh tokens additionally carry `fam` (family_id) for rotation
//     (BR-3.9). Access tokens never carry `fam`.
//   - Access TTL: 15 min. Refresh TTL: 30 days.
package jwt

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Tunables — exposed as constants so tests + ops docs can reference them.
const (
	// AccessTokenTTL is the lifetime of issued access tokens (BR-3.5).
	AccessTokenTTL = 15 * time.Minute

	// RefreshTokenTTL is the lifetime of issued refresh tokens (BR-3.5).
	RefreshTokenTTL = 30 * 24 * time.Hour

	// Audience is the canonical `aud` claim value on every issued token
	// (BR-3.6). api-gateway's JWT-verify middleware checks this.
	Audience = "he-api"

	// Algorithm is the only signing algorithm we accept. HS256 and "none"
	// are explicitly rejected. Configuration drift here would silently
	// degrade signature strength — keep it constant.
	Algorithm = "RS256"

	// MinRSAKeyBits is the minimum acceptable RSA modulus size. 4096 bits
	// (per BR-3.5 + Wright Round 1 Q3 ruling). The Signer constructor
	// refuses smaller keys.
	MinRSAKeyBits = 4096
)

// Errors returned by this package.
var (
	ErrInvalidAlgorithm   = errors.New("jwt: unexpected signing algorithm")
	ErrInvalidSignature   = errors.New("jwt: invalid signature")
	ErrExpired            = errors.New("jwt: token expired")
	ErrInvalidClaims      = errors.New("jwt: invalid claims")
	ErrKeyTooSmall        = errors.New("jwt: RSA key smaller than 4096 bits")
	ErrUnexpectedKeyType  = errors.New("jwt: unexpected key type (RSA required)")
)

// Claims is the minimal claim set every issued token carries. Refresh
// tokens additionally populate FamilyID; access tokens leave it empty.
//
// All field names follow the IANA JWT registry (sub/iat/exp/jti/aud) +
// a custom `fam` for refresh-family rotation per BR-3.9.
//
// Custom MarshalJSON is intentionally NOT defined — the implementation
// relies on the `omitempty` on FamilyID to exclude it from access tokens.
type Claims struct {
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	JTI       string `json:"jti"`
	Audience  string `json:"aud"`
	FamilyID  string `json:"fam,omitempty"`
}

// GetExpirationTime implements gojwt.Claims so the library can validate
// the exp claim during Parse.
func (c Claims) GetExpirationTime() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.ExpiresAt, 0)), nil
}

// GetIssuedAt implements gojwt.Claims.
func (c Claims) GetIssuedAt() (*gojwt.NumericDate, error) {
	return gojwt.NewNumericDate(time.Unix(c.IssuedAt, 0)), nil
}

// GetNotBefore implements gojwt.Claims (we don't issue `nbf`).
func (c Claims) GetNotBefore() (*gojwt.NumericDate, error) {
	return nil, nil
}

// GetIssuer implements gojwt.Claims (no `iss` claim — keeps payload small).
func (c Claims) GetIssuer() (string, error) {
	return "", nil
}

// GetSubject implements gojwt.Claims.
func (c Claims) GetSubject() (string, error) {
	return c.Subject, nil
}

// GetAudience implements gojwt.Claims.
func (c Claims) GetAudience() (gojwt.ClaimStrings, error) {
	return gojwt.ClaimStrings{c.Audience}, nil
}

// Signer issues RS256 tokens. Constructed once at auth-svc startup from
// the K8s Secret `he-api-auth-jwt-keys` mounted at /etc/auth-svc/keys/
// (Story 2.2 T0.5 Terraform-created).
type Signer struct {
	privateKey *rsa.PrivateKey
	keyID      string
}

// NewSigner parses + validates an RSA private key from PEM bytes.
// Returns ErrKeyTooSmall if the modulus is < MinRSAKeyBits.
func NewSigner(privateKeyPEM []byte) (*Signer, error) {
	key, err := gojwt.ParseRSAPrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse RSA private key: %w", err)
	}
	if key.N.BitLen() < MinRSAKeyBits {
		return nil, ErrKeyTooSmall
	}
	return &Signer{
		privateKey: key,
		keyID:      computeKeyID(&key.PublicKey),
	}, nil
}

// SignAccessToken issues an access token. `now` is the issuance time
// (overridable by tests for deterministic exp values). The 15-min TTL
// reflects BR-3.5.
func (s *Signer) SignAccessToken(userID uuid.UUID, now time.Time) (string, error) {
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("jwt: gen jti: %w", err)
	}
	claims := Claims{
		Subject:   userID.String(),
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(AccessTokenTTL).Unix(),
		JTI:       jti.String(),
		Audience:  Audience,
	}
	return s.sign(claims)
}

// SignRefreshToken issues a refresh token. familyID is the rotation
// bucket — all rotated refreshes for one session share a single family.
// The Redis tracker (auth:refresh:{family_id}:{jti}) lives at the handler
// layer; this function only embeds the value.
func (s *Signer) SignRefreshToken(userID uuid.UUID, familyID uuid.UUID, now time.Time) (string, error) {
	jti, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("jwt: gen jti: %w", err)
	}
	claims := Claims{
		Subject:   userID.String(),
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(RefreshTokenTTL).Unix(),
		JTI:       jti.String(),
		Audience:  Audience,
		FamilyID:  familyID.String(),
	}
	return s.sign(claims)
}

func (s *Signer) sign(c Claims) (string, error) {
	tok := gojwt.NewWithClaims(gojwt.SigningMethodRS256, c)
	tok.Header["kid"] = s.keyID
	return tok.SignedString(s.privateKey)
}

// KeyID returns the SHA-256-derived `kid` value used in the JWS header
// and the JWKS document. Stable across restarts for the same key.
func (s *Signer) KeyID() string {
	return s.keyID
}

// Verifier validates RS256 tokens against the RSA public key. Constructed
// from the ConfigMap `he-api-auth-public-keys` (Story 2.2 T0.5) — also
// used by api-gateway's JWT-verify middleware.
type Verifier struct {
	publicKey *rsa.PublicKey
	keyID     string
}

// NewVerifier parses an RSA public key from PEM bytes. RSA enforcement
// is via type assertion after ParsePKIXPublicKey.
func NewVerifier(publicKeyPEM []byte) (*Verifier, error) {
	key, err := gojwt.ParseRSAPublicKeyFromPEM(publicKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse RSA public key: %w", err)
	}
	if key.N.BitLen() < MinRSAKeyBits {
		return nil, ErrKeyTooSmall
	}
	return &Verifier{
		publicKey: key,
		keyID:     computeKeyID(key),
	}, nil
}

// Verify parses and validates the supplied token string. Returns the
// parsed Claims on success.
//
// Algorithm-confusion defense (BR-3.6, OWASP ASVS V3.5.5): the keyfunc
// callback inspects the JWS header's `alg` field and refuses anything
// other than RS256. A HS256-signed token submitted to this Verifier
// surfaces ErrInvalidAlgorithm — even if the attacker crafted a payload
// that would otherwise validate.
func (v *Verifier) Verify(tokenString string) (*Claims, error) {
	// NOTE: do NOT pass gojwt.WithValidMethods — its library-level check
	// short-circuits the keyfunc and surfaces a string-only error that's
	// awkward to map. Keeping the algorithm check inside the keyfunc means
	// our ErrInvalidAlgorithm sentinel propagates through errors.Is cleanly.
	parsed, err := gojwt.ParseWithClaims(tokenString, &Claims{}, func(t *gojwt.Token) (any, error) {
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
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidClaims
	}
	if claims.Audience != Audience {
		return nil, fmt.Errorf("%w: aud=%q", ErrInvalidClaims, claims.Audience)
	}
	return claims, nil
}

// KeyID returns the canonical `kid` for this verifier — matches
// Signer.KeyID() when constructed from a matching keypair.
func (v *Verifier) KeyID() string {
	return v.keyID
}

// JWKS returns the JSON Web Key Set document for /.well-known/jwks.json
// — exactly one RSA public key with kid + alg + use + kty + n + e.
// api-gateway exposes this endpoint per Wright Round 1 Q1 ruling.
func (v *Verifier) JWKS() ([]byte, error) {
	jwk := jwkRSA{
		Kty: "RSA",
		Use: "sig",
		Alg: Algorithm,
		Kid: v.keyID,
		N:   base64URLBigInt(v.publicKey.N),
		E:   base64URLBigInt(big.NewInt(int64(v.publicKey.E))),
	}
	doc := jwks{Keys: []jwkRSA{jwk}}
	return json.Marshal(doc)
}

// computeKeyID derives a short stable identifier from the public key.
// Format: first 16 hex chars of SHA-256(DER(pub)) — short enough for
// log readability, long enough to avoid accidental collisions.
func computeKeyID(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "" // never happens for valid RSA keys
	}
	sum := sha256.Sum256(der)
	return fmt.Sprintf("%x", sum[:8])
}

// base64URLBigInt encodes the absolute value of an integer per RFC 7518
// (base64url, no padding) — the format the JWK `n` and `e` fields require.
func base64URLBigInt(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}

type jwks struct {
	Keys []jwkRSA `json:"keys"`
}

type jwkRSA struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// pemBlock + parsePEM left for callers that want to hand-load keys.
// The Signer/Verifier constructors accept PEM bytes directly so most
// call sites won't need these.

// LoadPEM parses a PEM block from `data` and returns the decoded bytes
// (no type check — caller decides). Exists so the cmd/server wiring
// can fail loudly if the K8s Secret contents are malformed PEM.
func LoadPEM(data []byte) ([]byte, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: no PEM block found")
	}
	return block.Bytes, nil
}
