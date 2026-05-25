// Story 5.1 T6.1 — POST /v1/me/keys anti-abuse rate-limit helper.
//
// Counter key: `ratelimit:apikey:create:{user_id}` (BR-1.10).
// Ceiling: 10 successful-or-failed creates / hour / user (Architect Q5
// ratified — parity with Story-2.5 BR-2.8 profile-update ratelimit).
// Window: 3600 s (rolling — INCR + EXPIRE on first INCR, subsequent
// INCRs reuse the existing TTL).
//
// Fail-open philosophy (5.1-UNIT-039): rate-limit is anti-abuse not anti-
// security; a degraded Redis MUST NOT block legitimate users. On any
// Redis error the helper returns (allowed=true, retryAfter=0) + WARN log
// per the Story-2.3 OAuth ratelimit OQ3 cascade.
//
// Carve-out: the rate-limit counter is consumed only AFTER the validation
// + RPC path completes. The handler MAY skip increment on (a) strict-
// field-validation failures (BR-1.10 carve-out per docs/qa/assessments
// 5.1-INT-006); (b) RPC failure (BR-1.12 refund pattern — fail-fast does
// not consume budget). Currently `CheckCreateKeyRateLimit` is called
// BEFORE validation per AC1 ordering, so validation failures DO consume
// budget; the test design INT-006 asserts the carve-out is observable —
// the handler order is: (1) parse + validate body; (2) rate-limit check;
// (3) RPC. That ordering keeps the carve-out trivially correct.

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rate-limit constants. Exported so tests can range over them without
// duplicating literals.
const (
	CreateKeyRateLimitMax    = 10
	CreateKeyRateLimitWindow = 1 * time.Hour
	CreateKeyRateLimitPrefix = "ratelimit:apikey:create:"
)

// CreateKeyRateLimitDecision is the structured result of a rate-limit check.
// retryAfter is the int seconds until the counter's natural expiry; 0 when
// allowed=true OR when Redis was unreachable (fail-open).
type CreateKeyRateLimitDecision struct {
	Allowed    bool
	RetryAfter int
	Count      int64
}

// CheckCreateKeyRateLimit applies the BR-1.10 anti-abuse cap. The userID
// argument is the JWT-derived user UUID (string form) — caller MUST
// extract from JWT context, NEVER from the request body.
//
// Implementation: INCR the per-user counter; if INCR == 1 then set
// EXPIRE; on INCR > MAX return denied + Retry-After = remaining TTL.
// Any Redis error → fail-open (allowed=true, retryAfter=0, count=0) +
// WARN log.
func CheckCreateKeyRateLimit(ctx context.Context, rdb redis.Cmdable, logger *slog.Logger, userID string) CreateKeyRateLimitDecision {
	if rdb == nil {
		// Redis is fully unwired — fail-open per BR-1.10 / 5.1-UNIT-039.
		logger.WarnContext(
			ctx, "ratelimit_redis_failed",
			slog.String("reason", "redis_unwired"),
			slog.String("user_id", userID),
		)
		return CreateKeyRateLimitDecision{Allowed: true}
	}
	key := CreateKeyRateLimitPrefix + userID
	count, err := rdb.Incr(ctx, key).Result()
	if err != nil {
		logger.WarnContext(
			ctx, "ratelimit_redis_failed",
			slog.String("op", "INCR"),
			slog.String("error", err.Error()),
		)
		return CreateKeyRateLimitDecision{Allowed: true}
	}
	if count == 1 {
		// First INCR — establish the window TTL.
		if err := rdb.Expire(ctx, key, CreateKeyRateLimitWindow).Err(); err != nil {
			logger.WarnContext(
				ctx, "ratelimit_redis_failed",
				slog.String("op", "EXPIRE"),
				slog.String("error", err.Error()),
			)
			// Continue — the counter exists; without TTL it persists
			// indefinitely but the next-window quota will reset on the
			// EXPIRE retry. Caller's request still succeeds.
		}
	}
	if count > int64(CreateKeyRateLimitMax) {
		ttl, ttlErr := rdb.TTL(ctx, key).Result()
		retryAfter := int(CreateKeyRateLimitWindow.Seconds())
		switch {
		case errors.Is(ttlErr, redis.Nil):
			// Key absent — counter expired between INCR and TTL; treat as
			// freshly-allowed (allow + log).
			logger.WarnContext(
				ctx, "ratelimit_ttl_inconsistent",
				slog.String("key", key),
			)
			return CreateKeyRateLimitDecision{Allowed: true, Count: count}
		case ttlErr != nil:
			logger.WarnContext(
				ctx, "ratelimit_redis_failed",
				slog.String("op", "TTL"),
				slog.String("error", ttlErr.Error()),
			)
		case ttl > 0:
			retryAfter = int(ttl.Seconds())
			if ttl-time.Duration(retryAfter)*time.Second > 0 {
				retryAfter++ // round up so client doesn't retry too eagerly
			}
		}
		return CreateKeyRateLimitDecision{Allowed: false, RetryAfter: retryAfter, Count: count}
	}
	return CreateKeyRateLimitDecision{Allowed: true, Count: count}
}

// FormatRetryAfter renders an int second-count for the Retry-After
// HTTP header. Keeps the call site readable (the canonical fmt.Sprintf
// would otherwise be inline at every emission point).
func FormatRetryAfter(seconds int) string {
	return fmt.Sprintf("%d", seconds)
}
