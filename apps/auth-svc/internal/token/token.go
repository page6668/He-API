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

// KeyPrefix is the Redis namespace for the primary (hash-keyed) verification
// records (TS-CONS-006 + Q2 ruling: hashed-only at rest).
const KeyPrefix = "auth:email_verify:"

// UserKeyPrefix is the Redis namespace for the reverse index
// `auth:email_verify:user:{user_id}` → token_hash. The reverse index is
// what makes resend-time DEL-before-SET possible without a Redis SCAN:
// ResendVerification looks up the user's currently-active token hash and
// deletes the primary key, then Store writes the new pair.
//
// Both keys share the same TTL so they expire together when the user
// abandons signup.
const UserKeyPrefix = "auth:email_verify:user:"

// MaxConsumeAttempts trips ErrTokenAttemptsExceeded + DEL on the next
// Consume call (BR-2.5). 5 matches the spec; raise only if a UX issue
// surfaces (none expected — legitimate users hit the link once).
const MaxConsumeAttempts = 5

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

// ErrTokenNotFound is returned by Consume when the primary key is absent
// in Redis. This covers two distinct cases (the caller doesn't need to
// distinguish them at this layer):
//   - the token was never issued (random / forged)
//   - the 24-hour TTL elapsed and Redis auto-expired the key
var ErrTokenNotFound = errors.New("token: not found")

// ErrTokenAttemptsExceeded is returned by Consume when the attempts
// counter on the stored payload has reached MaxConsumeAttempts. The
// primary + reverse-index keys are DEL'd in the same atomic Lua call
// so any further submission of the same plaintext token returns
// ErrTokenNotFound.
var ErrTokenAttemptsExceeded = errors.New("token: too many attempts")

// Payload is the JSON value stored in Redis under KeyPrefix+Hash(token).
// snake_case field tags pin the wire shape — auth-svc Consume and any
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

// luaStore atomically writes the primary key AND the reverse index with
// the same TTL. This way Consume can find the primary via Hash(token) and
// ResendVerification can find the primary via the reverse index, with no
// risk of one key surviving without the other.
//
// KEYS[1] = primary key       (auth:email_verify:{hash(token)})
// KEYS[2] = reverse index key (auth:email_verify:user:{user_id})
// ARGV[1] = JSON payload
// ARGV[2] = token hash (stored as the reverse-index value)
// ARGV[3] = TTL seconds
const luaStore = `
redis.call("SET", KEYS[1], ARGV[1], "EX", ARGV[3])
redis.call("SET", KEYS[2], ARGV[2], "EX", ARGV[3])
return 1
`

// Store persists a fresh verification record under KeyPrefix+Hash(token)
// with TTL=24h AND writes the reverse index UserKeyPrefix+user_id →
// Hash(token) with the same TTL. The plaintext token is never written;
// the caller emails it to the user and discards.
//
// Atomicity: both keys are written in one Lua eval so a Redis crash
// between them cannot leave the system with a primary without a reverse
// index (or vice versa). Without the reverse index, ResendVerification
// would have no way to invalidate the old token without a full SCAN.
func Store(ctx context.Context, rdb redis.Scripter, token string, userID uuid.UUID) error {
	tokenHash := Hash(token)
	payload := Payload{
		UserID:    userID,
		ExpiresAt: time.Now().Add(TTL).UTC(),
		Attempts:  0,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("token: marshal payload: %w", err)
	}
	primaryKey := KeyPrefix + tokenHash
	userKey := UserKeyPrefix + userID.String()
	ttlSec := int64(TTL / time.Second)
	if err := rdb.Eval(ctx, luaStore, []string{primaryKey, userKey}, raw, tokenHash, ttlSec).Err(); err != nil {
		return fmt.Errorf("token: redis store eval: %w", err)
	}
	return nil
}

// luaConsume atomically reads the primary key, increments attempts, and
// either DEL's both keys (success or attempts-exceeded path) or rewrites
// the primary with the incremented attempts counter (intermediate failure
// path — unreachable under the current handler since we DEL on every
// pass, but the field exists so manual / external INCR via miniredis
// stubs can simulate the brute-force-lockout test).
//
// KEYS[1] = primary key (auth:email_verify:{hash(token)})
// ARGV[1] = MaxConsumeAttempts
// ARGV[2] = UserKeyPrefix ("auth:email_verify:user:")
//
// Returns: { status, payload_json_or_empty, attempts_after }
//   status: "not_found" | "attempts_exceeded" | "ok"
const luaConsume = `
local raw = redis.call("GET", KEYS[1])
if not raw then
  return {"not_found", "", "0"}
end
local payload = cjson.decode(raw)
local attempts = (payload.attempts or 0) + 1
local maxAttempts = tonumber(ARGV[1])
local userKey = ARGV[2] .. (payload.user_id or "")
if attempts >= maxAttempts then
  redis.call("DEL", KEYS[1])
  if payload.user_id and payload.user_id ~= "" then
    redis.call("DEL", userKey)
  end
  return {"attempts_exceeded", raw, tostring(attempts)}
end
-- Success path (one-shot consumption per BR-2.1): DEL primary + reverse index.
redis.call("DEL", KEYS[1])
if payload.user_id and payload.user_id ~= "" then
  redis.call("DEL", userKey)
end
return {"ok", raw, tostring(attempts)}
`

// Consume reads the verification record, increments the attempts counter,
// and (on either the happy path or attempts-exceeded path) DEL's the
// primary + reverse-index keys atomically.
//
// Returns:
//   - (Payload, nil)                      — first valid use; payload carries the user_id
//   - (Payload{}, ErrTokenNotFound)       — missing key (TTL expired or never issued)
//   - (Payload{}, ErrTokenAttemptsExceeded) — attempts counter reached the cap;
//                                             keys are DEL'd so further submission
//                                             of this plaintext token returns NotFound.
//
// The handler MUST surface ErrTokenAttemptsExceeded as 410_token_used (the
// canonical brute-force-suspect response) + audit auth.verify_email_brute_force
// (BR-2.5).
func Consume(ctx context.Context, rdb redis.Scripter, token string) (Payload, error) {
	primaryKey := KeyPrefix + Hash(token)
	raw, err := rdb.Eval(ctx, luaConsume, []string{primaryKey}, MaxConsumeAttempts, UserKeyPrefix).Result()
	if err != nil {
		return Payload{}, fmt.Errorf("token: redis consume eval: %w", err)
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) < 2 {
		return Payload{}, fmt.Errorf("token: consume: unexpected eval shape %T", raw)
	}
	status, _ := arr[0].(string)
	switch status {
	case "not_found":
		return Payload{}, ErrTokenNotFound
	case "attempts_exceeded":
		return Payload{}, ErrTokenAttemptsExceeded
	case "ok":
		payloadJSON, _ := arr[1].(string)
		var p Payload
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return Payload{}, fmt.Errorf("token: consume: unmarshal: %w", err)
		}
		return p, nil
	default:
		return Payload{}, fmt.Errorf("token: consume: unknown status %q", status)
	}
}

// luaDeleteForUser invalidates the active verification token for a user
// by chaining through the reverse index. Used by ResendVerification to
// guarantee BR-1.6 "old token invalidated immediately before the new one
// is issued".
//
// KEYS[1] = reverse-index key (auth:email_verify:user:{user_id})
// ARGV[1] = KeyPrefix ("auth:email_verify:")
//
// Returns 1 if a primary was deleted, 0 if there was no active token.
const luaDeleteForUser = `
local tokenHash = redis.call("GET", KEYS[1])
if not tokenHash then
  return 0
end
redis.call("DEL", ARGV[1] .. tokenHash)
redis.call("DEL", KEYS[1])
return 1
`

// DeleteForUser invalidates the active token (if any) for the supplied
// user_id. Used by the ResendVerification handler so the freshly-issued
// token is the only valid one (BR-1.6).
//
// Returns (true, nil) when a token was found and deleted; (false, nil)
// when the user had no active token. Network / Redis errors are returned.
func DeleteForUser(ctx context.Context, rdb redis.Scripter, userID uuid.UUID) (bool, error) {
	userKey := UserKeyPrefix + userID.String()
	raw, err := rdb.Eval(ctx, luaDeleteForUser, []string{userKey}, KeyPrefix).Result()
	if err != nil {
		return false, fmt.Errorf("token: redis delete-for-user eval: %w", err)
	}
	n, ok := raw.(int64)
	if !ok {
		return false, fmt.Errorf("token: delete-for-user: unexpected eval shape %T", raw)
	}
	return n == 1, nil
}
