// usage_log_export.go — Story 9.3 BR-EX-5 usage-log export rate-limit safety
// net. Mirrors the GDPR-export limiter EXACTLY (same atomic Lua INCR+EXPIRE
// race-guard) but under a SEPARATE key namespace so usage-log exports do NOT
// consume the GDPR-export 24h quota (and vice-versa).
//
// Key pattern: `ratelimit:usage_log_export:{user_id}` (distinct from
// `ratelimit:gdpr:export:{user_id}`). TTL: 86400s. Rejection path: INCR > 1.
//
// As with the GDPR limiter, the authoritative idempotency check is the PG row
// lookup (repository.FindCurrentUsageLogInWindow); this Redis counter is the
// SAFETY NET against the ~100ms race where two concurrent POSTs both miss the
// PG check before the row commits.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyPrefixUsageLogExport is the Redis key prefix for the usage-log export
// counter (BR-EX-5). SEPARATE namespace from KeyPrefixGDPRExport.
const KeyPrefixUsageLogExport = "ratelimit:usage_log_export:"

// WindowUsageLogExport is the rolling-window TTL (24h, reuse the 2.6 default).
const WindowUsageLogExport = 24 * time.Hour

// UsageLogExportLimiter wraps a Redis client for the usage-log export counter.
type UsageLogExportLimiter struct {
	client *redis.Client
}

// NewUsageLogExportLimiter constructs a limiter against the supplied client.
func NewUsageLogExportLimiter(client *redis.Client) *UsageLogExportLimiter {
	return &UsageLogExportLimiter{client: client}
}

// CheckAndIncr atomically increments the per-user usage-log-export counter.
// Returns ErrRateLimited (shared sentinel) when the post-INCR value > 1.
func (l *UsageLogExportLimiter) CheckAndIncr(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("ratelimit: user_id required")
	}
	key := KeyPrefixUsageLogExport + userID
	count, err := l.client.Eval(ctx, luaIncrOnce, []string{key}, int(WindowUsageLogExport.Seconds())).Int64()
	if err != nil {
		return fmt.Errorf("ratelimit: redis eval: %w", err)
	}
	if count > 1 {
		return ErrRateLimited
	}
	return nil
}

// Reset undoes a CheckAndIncr when the surrounding PG transaction rolls back.
// Best-effort.
func (l *UsageLogExportLimiter) Reset(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	key := KeyPrefixUsageLogExport + userID
	return l.client.Del(ctx, key).Err()
}
