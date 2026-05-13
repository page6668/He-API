// Package oauth implements the Story 2.3 OAuth (Google + GitHub) flow
// helpers. P1 lands StateService — the Redis-backed state + PKCE store
// behind the Authorization Code + PKCE flow.
//
// Wright Round 1 rulings consumed here:
//   - Q1=(a): Redis-only state with hashed-key namespace
//     `auth:oauth:state:{sha256(state_id)}`, TTL=600s, one-shot GETDEL.
//   - Q4=(b)/Constraint (c): PKCE S256 challenge_method only (no plain).
//   - m-4 ruling: IP binding hashes the /24 prefix (IPv4) or /64 prefix
//     (IPv6) — the CGNAT / Wi-Fi-switch tolerance trade-off. UA is full
//     string. The narrower binding still composes with the Redis sha256
//     match + the api-gateway cookie binding for triple defense.
//
// Invariants:
//   - state_id is 32 random bytes from crypto/rand → 43-char base64url
//     unpadded (≥ 256-bit entropy).
//   - pkce_verifier is 32 random bytes → 43 base64url chars (RFC 7636
//     §4.1 lower bound; spec range is [43, 128]).
//   - pkce_challenge = base64url(SHA-256(verifier)) per RFC 7636 §4.2
//     S256 method.
//   - The plaintext state_id is returned to the caller exactly once for
//     inclusion in the provider authorize URL + the `he_oauth_state`
//     cookie; storage key uses lowercase-hex SHA-256(state_id) so the
//     plaintext never lives in Redis after issuance (Story 2.2 token-pkg
//     hashed-only-at-rest convention).
//   - ConsumeState is one-shot (GETDEL); a second call within TTL
//     returns ErrStateNotFound regardless of validation outcome.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// StateTTL is the canonical state window (Wright Round 1 Q1 ruling).
// The handler passes this value to NewState; centralising the constant
// keeps tests and metrics aligned.
const StateTTL = 10 * time.Minute

// StateKeyPrefix is the Redis namespace per BR-1.1 hashed-key convention.
// data-models.md §4.3 lists this prefix alongside auth:email_verify:*
// (Story 2.2) and ratelimit:* (Story 1.6) — the Architect M-2 doc-drift
// follow-up registers it formally.
const StateKeyPrefix = "auth:oauth:state:"

// stateEntropyBytes / pkceVerifierBytes — 32 bytes of crypto/rand each;
// base64url-unpadded length is 43 chars (32×8/6 ceiling). Above the
// 256-bit security level RFC 6749 §10.12 + RFC 7636 §4.1 require.
const (
	stateEntropyBytes = 32
	pkceVerifierBytes = 32
)

// Sentinel errors. Each maps to the same user-facing 400_oauth_state_invalid
// HTTP response (anti-info-leak per BR-1.3 / m-5 anti-enumeration pattern);
// distinct values let the audit publisher emit precise reason codes:
//   - ErrStateNotFound          → reason="state_not_found"
//   - ErrStateExpired           → reason="state_expired" (clock-skew edge)
//   - ErrProviderMismatch       → reason="provider_mismatch"
//   - ErrClientBindingMismatch  → reason="binding_mismatch"
var (
	ErrStateNotFound         = errors.New("oauth: state not found")
	ErrStateExpired          = errors.New("oauth: state expired")
	ErrProviderMismatch      = errors.New("oauth: provider mismatch")
	ErrClientBindingMismatch = errors.New("oauth: client binding mismatch")
)

// StatePayload is the JSON value stored under StateKeyPrefix+sha256(state_id).
// snake_case tags pin the on-wire shape — audit + debug tooling read the
// same shape across deployments.
//
// PKCEVerifier is server-side only. Callers MUST NOT echo it back to the
// client; it is consumed by the auth-svc token-exchange step.
type StatePayload struct {
	Provider     string    `json:"provider"`
	PKCEVerifier string    `json:"pkce_verifier"`
	ReturnTo     string    `json:"return_to"`
	IPHash       string    `json:"ip_hash"`
	UAHash       string    `json:"ua_hash"`
	Locale       string    `json:"locale"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Service satisfies StateService. Constructed via NewService; cmd/server
// wires the live redis.Client, tests inject a miniredis-backed client.
type Service struct {
	rdb redis.Cmdable
	now func() time.Time
}

// NewService builds a StateService backed by the supplied Redis client.
// `now` defaults to time.Now; tests may override via WithNow for
// deterministic expiry assertions.
func NewService(rdb redis.Cmdable) *Service {
	return &Service{rdb: rdb, now: time.Now}
}

// WithNow overrides the clock — useful for deterministic test assertions
// against CreatedAt / ExpiresAt fields. Production code never calls this.
func (s *Service) WithNow(now func() time.Time) *Service {
	s.now = now
	return s
}

// NewState generates a state_id + PKCE verifier + S256 challenge, then
// persists the payload to Redis under the hashed key with the supplied
// TTL. Returns the raw state_id (for cookie + provider authorize URL)
// and the PKCE challenge (for provider authorize URL); the verifier
// itself is server-side only.
func (s *Service) NewState(ctx context.Context, payload StatePayload, ttl time.Duration) (stateID, pkceChallenge string, err error) {
	stateBytes := make([]byte, stateEntropyBytes)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", "", fmt.Errorf("oauth: state entropy: %w", err)
	}
	stateID = base64.RawURLEncoding.EncodeToString(stateBytes)

	verifierBytes := make([]byte, pkceVerifierBytes)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("oauth: verifier entropy: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	pkceChallenge = base64.RawURLEncoding.EncodeToString(sum[:])

	now := s.now().UTC()
	stored := payload
	stored.PKCEVerifier = verifier
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = now
	}
	stored.ExpiresAt = now.Add(ttl)

	raw, err := json.Marshal(stored)
	if err != nil {
		return "", "", fmt.Errorf("oauth: marshal state: %w", err)
	}
	if err := s.rdb.Set(ctx, stateKey(stateID), raw, ttl).Err(); err != nil {
		return "", "", fmt.Errorf("oauth: redis set state: %w", err)
	}
	return stateID, pkceChallenge, nil
}

// ConsumeState performs the one-shot GETDEL, validates provider + client
// binding, and returns the payload on full match.
//
// Validation order matters for audit precision: provider mismatch is
// detected before binding mismatch so a forged-provider state never
// reaches the IP+UA comparison path. All sentinels map to the same
// 400_oauth_state_invalid user response — caller distinguishes them
// only when emitting the audit reason field.
func (s *Service) ConsumeState(ctx context.Context, stateID, expectedProvider, currentIP, currentUA string) (StatePayload, error) {
	raw, err := s.rdb.GetDel(ctx, stateKey(stateID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return StatePayload{}, ErrStateNotFound
		}
		return StatePayload{}, fmt.Errorf("oauth: redis getdel state: %w", err)
	}
	if raw == "" {
		return StatePayload{}, ErrStateNotFound
	}
	var p StatePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return StatePayload{}, fmt.Errorf("oauth: unmarshal state: %w", err)
	}
	// Edge: a stale payload survived Redis TTL eviction (clock skew, or a
	// test that injected an expired payload). The user response is the
	// same as not-found; the sentinel lets audit distinguish.
	if !p.ExpiresAt.IsZero() && s.now().After(p.ExpiresAt) {
		return StatePayload{}, ErrStateExpired
	}
	if expectedProvider != "" && p.Provider != expectedProvider {
		return StatePayload{}, ErrProviderMismatch
	}
	if p.IPHash != HashClientIP(currentIP) || p.UAHash != HashUserAgent(currentUA) {
		return StatePayload{}, ErrClientBindingMismatch
	}
	return p, nil
}

// HashClientIP normalises per Wright Round 1 m-4 ruling and returns the
// lowercase-hex SHA-256 of the prefix:
//   - IPv4 → /24 prefix (first 3 octets)
//   - IPv6 → /64 prefix (first 8 bytes)
//   - malformed input → SHA-256 of the raw bytes (defensive fallback;
//     binding still works as long as initiate + callback see the same
//     malformed value)
//
// Exported so api-gateway + tests share the exact normalisation logic.
func HashClientIP(clientIP string) string {
	ip := net.ParseIP(clientIP)
	if ip == nil {
		sum := sha256.Sum256([]byte(clientIP))
		return hex.EncodeToString(sum[:])
	}
	var prefix []byte
	if v4 := ip.To4(); v4 != nil {
		prefix = v4[:3]
	} else {
		prefix = ip.To16()[:8]
	}
	sum := sha256.Sum256(prefix)
	return hex.EncodeToString(sum[:])
}

// HashUserAgent SHA-256-hashes the full UA string. Browser UA strings
// stay stable across a single OAuth round-trip (initiate→callback is
// seconds), so the strict full-string match composes safely with the
// looser /24 IP prefix per m-4.
func HashUserAgent(userAgent string) string {
	sum := sha256.Sum256([]byte(userAgent))
	return hex.EncodeToString(sum[:])
}

// HashSubject returns the lowercase-hex SHA-256 of the provider subject
// (Google `sub` claim or GitHub user `id`). Used to populate
// `Metadata.subject_hash` on OAuth audit events per BR-3.9 / BR-4.5 so
// downstream analytics can correlate identities without storing raw
// subject values. Empty string in → empty string out (no spurious hash
// of nothing).
func HashSubject(subject string) string {
	if subject == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

// stateKey returns the canonical Redis key for the plaintext state_id.
// Keep symmetric with Story 2.2 token.Hash: lowercase hex SHA-256 so
// data-models.md §4.3 can register the namespace once.
func stateKey(stateID string) string {
	sum := sha256.Sum256([]byte(stateID))
	return StateKeyPrefix + hex.EncodeToString(sum[:])
}
