// Package ratelimit implements the GDPR export rate-limit safety net
// (Story 2.6 AC2 BR-2.5 + AC6 BR-6.4).
//
// The authoritative idempotency check is the PG row lookup in
// repository.DataExportRequestsRepo.FindCurrentInWindow; this Redis
// rate-limit is the SAFETY NET against the ~100ms race-condition window
// where two concurrent requests could both miss the PG check before the
// row commits.
//
// Key pattern: `ratelimit:gdpr:export:{user_id}` (AC6 BR-6.4 / TS-CONS-007).
// TTL: 86400s. Counter: INCR semantics; rejection path is INCR > 1.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyPrefixGDPRExport is the Redis key prefix for the GDPR-export
// counter (AC6 BR-6.4 — registered in data-models.md §4.3 by Story 2.6).
const KeyPrefixGDPRExport = "ratelimit:gdpr:export:"

// WindowGDPRExport is the rolling-window TTL per BR-6.4.
const WindowGDPRExport = 24 * time.Hour

// ErrRateLimited indicates the post-INCR counter is > 1, i.e. the race
// safety-net fired.
var ErrRateLimited = errors.New("ratelimit: gdpr export rate limit exceeded")

// luaIncrOnce atomically issues INCR + EXPIRE (only on first increment).
// Returns the post-INCR count. The script runs server-side so the two
// commands cannot be split by a crash.
const luaIncrOnce = `
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return count
`

// GDPRExportLimiter wraps a Redis client for the GDPR-export counter.
type GDPRExportLimiter struct {
	client *redis.Client
}

// NewGDPRExportLimiter constructs a limiter against the supplied client.
func NewGDPRExportLimiter(client *redis.Client) *GDPRExportLimiter {
	return &GDPRExportLimiter{client: client}
}

// CheckAndIncr atomically increments the per-user counter. Returns
// ErrRateLimited when the post-INCR value > 1 (the race safety-net hit).
// Caller MUST roll back any PG transaction it had open and surface 429.
//
// userID is appended as-is to KeyPrefixGDPRExport — caller validates the
// UUID shape before passing in (BR-6.4 key-pattern validation).
func (l *GDPRExportLimiter) CheckAndIncr(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("ratelimit: user_id required")
	}
	key := KeyPrefixGDPRExport + userID
	count, err := l.client.Eval(ctx, luaIncrOnce, []string{key}, int(WindowGDPRExport.Seconds())).Int64()
	if err != nil {
		return fmt.Errorf("ratelimit: redis eval: %w", err)
	}
	if count > 1 {
		return ErrRateLimited
	}
	return nil
}

// Reset is used by the data_export handler to undo a CheckAndIncr when
// the surrounding PG transaction rolls back (so a Redis hit doesn't
// orphan the counter for the rest of the 24h window). Best-effort —
// warn-log on failure but do not surface to caller.
func (l *GDPRExportLimiter) Reset(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	key := KeyPrefixGDPRExport + userID
	return l.client.Del(ctx, key).Err()
}
