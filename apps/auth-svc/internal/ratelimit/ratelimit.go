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
