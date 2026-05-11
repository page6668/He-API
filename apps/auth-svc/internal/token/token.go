package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// TTL is the canonical email-verification window (BR-1.6, Wright Round 1 Q2
// ruling). Plaintext token is short-lived; Redis auto-expires when the user
// abandons signup.
const TTL = 24 * time.Hour

// KeyPrefix is the Redis namespace for stored verification tokens
// (TS-CONS-006 + Q2 ruling: hashed-only at rest).
const KeyPrefix = "auth:email_verify:"

// tokenEntropyBytes is the CSPRNG input size. 48 bytes → 64 base64url chars
// (no padding) = 384 bits of entropy, far above the 256-bit security level
// required for one-shot verification tokens.
//
// Note on the descriptive "32 bytes" in 2.2-UNIT-070: 32 bytes base64url is
// 43 chars, which contradicts the same scenario's 64-char output assertion
// and UNIT-073's strict length=64 requirement. The structural invariants
// (length=64, charset [A-Za-z0-9_-]) take precedence — they are what the
// downstream contract enforces (mailer link width, URL parser limits).
const tokenEntropyBytes = 48

// tokenFormat matches the canonical 64-char base64url unpadded form.
var tokenFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{64}$`)

// ErrInvalidFormat is returned by ParseFormat for any input that does not
// match the canonical 64-char base64url-unpadded shape.
var ErrInvalidFormat = errors.New("token: invalid format")

// Payload is the JSON value stored in Redis under KeyPrefix+Hash(token).
// snake_case field tags pin the wire shape — auth-svc Consume (P3) and any
// audit / debug tooling read the same shape across deployments.
type Payload struct {
	UserID    uuid.UUID `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Attempts  int       `json:"attempts"`
}

// Generate returns a fresh cryptographically random verification token.
// 48 random bytes → 64 base64url-unpadded chars. The CSPRNG (crypto/rand)
// is required by TS-CONS-006; math/rand is forbidden (2.2-UNIT-071).
func Generate() (string, error) {
	b := make([]byte, tokenEntropyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns the lowercase hex of SHA-256(token). The hash is what is
// persisted in Redis — the plaintext token never lives server-side after
// it has been emailed to the user (TS-CONS-006, OWASP ASVS L2 V3.4.1).
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ParseFormat validates that token conforms to the canonical 64-char
// base64url-unpadded shape (2.2-UNIT-073). The handler MUST call this
// before any Redis lookup — it short-circuits malformed tokens at the
// edge, saving a round-trip and surfacing a clean 400_invalid_token.
func ParseFormat(token string) error {
	if !tokenFormat.MatchString(token) {
		return ErrInvalidFormat
	}
	return nil
}

// Store persists a fresh verification record under KeyPrefix+Hash(token)
// with TTL=24h. The plaintext token is never written; the caller emails it
// to the user and discards.
//
// Overwrites any existing record at the same hashed key (rare — collisions
// require a SHA-256 break). Resend-time invalidation of a prior token for
// the same user is the resend handler's responsibility (BR-1.6, lands in
// P3) — at the storage primitive layer we apply only the simple SET-EX
// contract 2.2-UNIT-074 specifies.
func Store(ctx context.Context, rdb redis.Cmdable, token string, userID uuid.UUID) error {
	payload := Payload{
		UserID:    userID,
		ExpiresAt: time.Now().Add(TTL).UTC(),
		Attempts:  0,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("token: marshal payload: %w", err)
	}
	if err := rdb.Set(ctx, KeyPrefix+Hash(token), raw, TTL).Err(); err != nil {
		return fmt.Errorf("token: redis SET: %w", err)
	}
	return nil
}
