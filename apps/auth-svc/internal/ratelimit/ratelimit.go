package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Five Redis key prefixes (BR-4.1 — auth-svc rate-limit matrix). The literal
// strings appear in source so the QA static scan (2.2-UNIT-173) can grep
// them; they are also surfaced via the five key-builder functions below.
const (
	keyPrefixSignupIP    = "ratelimit:signup:ip:"
	keyPrefixSigninIP    = "ratelimit:signin:ip:"
	keyPrefixSigninEmail = "ratelimit:signin:email:"
	keyPrefixResendIP    = "ratelimit:resend:ip:"
	keyPrefixResendEmail = "ratelimit:resend:email:"

	// Story 2.4 — five 2FA rate-limit namespaces (per-user-only per Wright
	// Round 1 Q5 ruling). Counter TTL matches the window arg passed to
	// CheckAndIncr; handlers surface 429 via Result.RetryAfter.
	KeyPrefix2FAEnrollInit    = "ratelimit:2fa:enroll:init:"
	KeyPrefix2FAEnrollVerify  = "ratelimit:2fa:enroll:verify:"
	KeyPrefix2FAChallenge     = "ratelimit:2fa:challenge:"
	KeyPrefix2FARecovery      = "ratelimit:2fa:recovery:"
	KeyPrefix2FADisable       = "ratelimit:2fa:disable:"
	// Story 2.5 — profile-update rate-limit key (BR-2.8 — 10 attempts/hour
	// per user). Per-user-only (no per-IP layer); matches the dimensionality
	// of other authenticated-mutation endpoints.
	KeyPrefixProfileUpdate = "ratelimit:profile:update:"
)

// ErrRateLimited is returned by CheckAndIncr when the post-INCR count exceeds
// the supplied limit. The Result.RetryAfter field carries the remaining
// window (translated by the handler into the HTTP `Retry-After` header per
// BR-1.7 / BR-3.8 / BR-2.4).
var ErrRateLimited = errors.New("ratelimit: limit exceeded")

// Result reports the outcome of a CheckAndIncr call.
type Result struct {
	// Count is the post-INCR counter value (1-based).
	Count int64
	// RetryAfter is populated only when err == ErrRateLimited; it reflects the
	// remaining TTL on the counter key, rounded up to seconds at the wire.
	RetryAfter time.Duration
}

// luaCheckAndIncr atomically issues INCR + (first-call) EXPIRE. The return
// value is { count, ttl_seconds }. The script runs server-side so the two
// commands cannot be split by a crash between them (BR-4.1 + 2.2-UNIT-170
// atomicity).
//
// We deliberately do NOT use MULTI/EXEC: under network partition, EXEC has
// optimistic semantics (WATCH); a single Lua script guarantees serializability
// at the Redis side without that complication. miniredis's gopher-lua runtime
// supports this script identically to real Redis.
const luaCheckAndIncr = `
local count = redis.call("INCR", KEYS[1])
if count == 1 then
    redis.call("EXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("TTL", KEYS[1])
return {count, ttl}
`

// CheckAndIncr atomically increments the counter at key and applies the
// supplied TTL on the first hit. Returns ErrRateLimited when the counter
// exceeds limit; the handler MUST surface that as 429 with Retry-After.
//
// The Lua script runs as one Redis command so two concurrent goroutines /
// pods cannot both observe `count==1` and lose the EXPIRE on one side.
//
// `window` MUST be > 0 and < 100 years; values outside that produce undefined
// Redis behavior.
func CheckAndIncr(ctx context.Context, rdb redis.Scripter, key string, limit int64, window time.Duration) (Result, error) {
	windowSec := int64(window / time.Second)
	if windowSec <= 0 {
		windowSec = 1
	}
	raw, err := rdb.Eval(ctx, luaCheckAndIncr, []string{key}, windowSec).Result()
	if err != nil {
		return Result{}, fmt.Errorf("ratelimit: eval: %w", err)
	}
	count, ttl, err := unpackEvalResult(raw)
	if err != nil {
		return Result{}, err
	}
	res := Result{Count: count}
	if count > limit {
		retry := time.Duration(ttl) * time.Second
		if retry <= 0 {
			// Key expired between INCR and TTL (rare race). Conservative
			// fallback: report the configured window so the caller still
			// surfaces a sensible Retry-After.
			retry = window
		}
		res.RetryAfter = retry
		return res, ErrRateLimited
	}
	return res, nil
}

// unpackEvalResult tolerates the two array shapes returned by go-redis: the
// canonical []interface{}{int64, int64} and, defensively, any reordering.
func unpackEvalResult(raw any) (int64, int64, error) {
	arr, ok := raw.([]any)
	if !ok || len(arr) < 2 {
		return 0, 0, fmt.Errorf("ratelimit: unexpected eval result shape %T", raw)
	}
	count, ok1 := arr[0].(int64)
	ttl, ok2 := arr[1].(int64)
	if !ok1 || !ok2 {
		return 0, 0, fmt.Errorf("ratelimit: non-int64 in eval result: %v / %v", arr[0], arr[1])
	}
	return count, ttl, nil
}

// EmailHash returns the lowercase hex of SHA-256(strings.ToLower(strings.TrimSpace(email)))
// (BR-4.2). The same algorithm is used for audit event payloads so a key
// observed in Redis can be joined against an audit entry without exposing
// the plaintext email anywhere.
func EmailHash(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// SignupIPKey returns the BR-1.7 signup IP rate-limit key.
func SignupIPKey(ip string) string {
	return keyPrefixSignupIP + ip
}

// SigninIPKey returns the BR-3.8 signin IP rate-limit key.
func SigninIPKey(ip string) string {
	return keyPrefixSigninIP + ip
}

// SigninEmailKey returns the BR-3.8 signin email rate-limit key. email_hash
// is computed via EmailHash so the raw email never appears in any Redis key
// (TS-CONS-005 + 2.2-UNIT-174).
func SigninEmailKey(email string) string {
	return keyPrefixSigninEmail + EmailHash(email)
}

// ResendIPKey returns the BR-2.4 resend-verification IP rate-limit key.
func ResendIPKey(ip string) string {
	return keyPrefixResendIP + ip
}

// ResendEmailKey returns the BR-2.4 resend-verification per-email rate-limit
// key — email_hash, not plaintext.
func ResendEmailKey(email string) string {
	return keyPrefixResendEmail + EmailHash(email)
}

// MFA2FAOperation enumerates the five 2FA rate-limit namespaces. Spelled as a
// distinct type so the handler call site cannot accidentally swap an
// operation tag for an unrelated string.
type MFA2FAOperation string

const (
	OpMFAEnrollInit   MFA2FAOperation = "enroll_init"
	OpMFAEnrollVerify MFA2FAOperation = "enroll_verify"
	OpMFAChallenge    MFA2FAOperation = "challenge"
	OpMFARecovery     MFA2FAOperation = "recovery"
	OpMFADisable      MFA2FAOperation = "disable"
)

// MFAKey builds the per-user rate-limit key for the supplied 2FA operation
// (BR-5.1). user_id is the canonical UUID string; callers MUST pass the same
// representation across init and verify so the counter shares a key.
//
// Per Wright Round 1 Q5 ruling, this is per-user-only (no per-IP layer);
// rate-limit dimensionality matches Story 2.2 signin and Story 2.3 OAuth.
// ProfileUpdateKey builds the per-user rate-limit key for Story 2.5 PUT
// /v1/me/profile (BR-2.8 — 10 successful-or-failed updates / hour / user;
// anti-abuse, not anti-error). userID is the canonical UUID string.
func ProfileUpdateKey(userID string) string {
	return KeyPrefixProfileUpdate + userID
}

func MFAKey(op MFA2FAOperation, userID string) string {
	switch op {
	case OpMFAEnrollInit:
		return KeyPrefix2FAEnrollInit + userID
	case OpMFAEnrollVerify:
		return KeyPrefix2FAEnrollVerify + userID
	case OpMFAChallenge:
		return KeyPrefix2FAChallenge + userID
	case OpMFARecovery:
		return KeyPrefix2FARecovery + userID
	case OpMFADisable:
		return KeyPrefix2FADisable + userID
	default:
		// Caller bug; surface a deterministic key prefix that grep'd
		// audits will catch. Never reached on a well-typed call.
		return "ratelimit:2fa:unknown:" + userID
	}
}
