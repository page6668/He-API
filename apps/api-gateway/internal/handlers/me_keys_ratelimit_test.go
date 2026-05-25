// Story 5.1 — UNIT-036..040 (POST /v1/me/keys rate-limit helper).
// See docs/qa/assessments/5.1-test-design-20260525.md.

package handlers

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newRateLimitHarness wires a Redis client backed by miniredis + a discard
// logger; tests can inject a buffer logger when they need to assert WARN
// records.
func newRateLimitHarness(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mini, rdb
}

// TestCheckCreateKeyRateLimit covers UNIT-036..040.
// Source: T6.1 (story line 584-590) + design-doc §"Unit: Rate-limit".
func TestCheckCreateKeyRateLimit(t *testing.T) {
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	userID := "00000000-0000-4000-8000-000000000001"

	t.Run("5.1-UNIT-036 allowed for 1..10 calls + INCR + EXPIRE 3600", func(t *testing.T) {
		// Scenario: 5.1-UNIT-036
		// Priority: P0
		// Input:    miniredis empty counter; 10 successive calls
		// Expected: each returns (allowed=true, retryAfter=0); INCR counter reaches 10;
		//           EXPIRE set with TTL 3600.
		// BR-1.10 allowed range
		mini, rdb := newRateLimitHarness(t)
		for i := 1; i <= CreateKeyRateLimitMax; i++ {
			d := CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
			if !d.Allowed {
				t.Fatalf("call %d: Allowed=false want true (count=%d)", i, d.Count)
			}
			if d.RetryAfter != 0 {
				t.Fatalf("call %d: RetryAfter=%d want 0", i, d.RetryAfter)
			}
			if d.Count != int64(i) {
				t.Fatalf("call %d: Count=%d want %d", i, d.Count, i)
			}
		}
		// INCR counter reached 10; EXPIRE TTL ≈ 3600s.
		key := CreateKeyRateLimitPrefix + userID
		ttl := mini.TTL(key)
		if ttl.Seconds() < 3500 || ttl.Seconds() > 3700 {
			t.Fatalf("ttl=%v want ~3600s", ttl)
		}
	})

	t.Run("5.1-UNIT-037 denied at 11th call with non-zero Retry-After", func(t *testing.T) {
		// Scenario: 5.1-UNIT-037
		// Priority: P0
		// Input:    counter already at 10; 11th call
		// Expected: returns (allowed=false, retryAfter>0); retryAfter equals key's remaining TTL.
		// BR-1.10 ceiling
		_, rdb := newRateLimitHarness(t)
		for i := 0; i < CreateKeyRateLimitMax; i++ {
			_ = CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		}
		d := CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		if d.Allowed {
			t.Fatalf("11th call: Allowed=true want false")
		}
		if d.RetryAfter <= 0 {
			t.Fatalf("11th call: RetryAfter=%d want > 0", d.RetryAfter)
		}
		if d.RetryAfter > int(CreateKeyRateLimitWindow.Seconds())+5 {
			t.Fatalf("11th call: RetryAfter=%d exceeds window+5 = %d", d.RetryAfter, int(CreateKeyRateLimitWindow.Seconds())+5)
		}
	})

	t.Run("5.1-UNIT-038 retryAfter accuracy after 1800s clock advance", func(t *testing.T) {
		// Scenario: 5.1-UNIT-038
		// Priority: P1
		// Input:    advance miniredis clock by 1800s after counter SET
		// Expected: 11th call returns retryAfter ~ 1800.
		// BR-1.10 Retry-After semantics
		mini, rdb := newRateLimitHarness(t)
		for i := 0; i < CreateKeyRateLimitMax; i++ {
			_ = CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		}
		mini.FastForward(1800 * time.Second)
		d := CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		if d.Allowed {
			t.Fatalf("11th call: Allowed=true want false")
		}
		// After 1800s the remaining TTL should be ~1800; allow ±5s tolerance.
		if d.RetryAfter < 1790 || d.RetryAfter > 1810 {
			t.Fatalf("RetryAfter=%d want ~1800 (±10)", d.RetryAfter)
		}
	})

	t.Run("5.1-UNIT-039 redis error fails OPEN + WARN log", func(t *testing.T) {
		// Scenario: 5.1-UNIT-039
		// Priority: P0
		// Input:    miniredis closed mid-test (returns connection error)
		// Expected: returns (allowed=true, retryAfter=0); slog WARN
		//           event="ratelimit_redis_failed".
		// Fail-open philosophy per Story-2.3 OQ3 cascade
		mini, rdb := newRateLimitHarness(t)
		var logBuf strings.Builder
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))
		// First call succeeds.
		if d := CheckCreateKeyRateLimit(context.Background(), rdb, logger, userID); !d.Allowed {
			t.Fatalf("pre-close: Allowed=false")
		}
		// Close miniredis to simulate Redis outage.
		mini.Close()
		d := CheckCreateKeyRateLimit(context.Background(), rdb, logger, userID)
		if !d.Allowed {
			t.Fatalf("post-close: Allowed=false want true (fail-open)")
		}
		if d.RetryAfter != 0 {
			t.Fatalf("post-close: RetryAfter=%d want 0", d.RetryAfter)
		}
		if !strings.Contains(logBuf.String(), "ratelimit_redis_failed") {
			t.Fatalf("logBuf missing ratelimit_redis_failed WARN: %s", logBuf.String())
		}
	})

	t.Run("5.1-UNIT-040 counter natural expiry after 3601s", func(t *testing.T) {
		// Scenario: 5.1-UNIT-040
		// Priority: P1
		// Input:    INCR + EXPIRE 3600, then advance miniredis TIME by 3601s
		// Expected: counter key absent; next call resets counter to 1 (allowed=true).
		// BR-1.10 TTL semantics
		mini, rdb := newRateLimitHarness(t)
		_ = CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		mini.FastForward(3601 * time.Second)
		d := CheckCreateKeyRateLimit(context.Background(), rdb, discard, userID)
		if !d.Allowed {
			t.Fatalf("post-expiry: Allowed=false want true")
		}
		if d.Count != 1 {
			t.Fatalf("post-expiry: Count=%d want 1 (counter reset)", d.Count)
		}
	})

	t.Run("redis nil — fully fail-open", func(t *testing.T) {
		// Defensive: factory returned nil (no Redis at all).
		var logBuf strings.Builder
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))
		d := CheckCreateKeyRateLimit(context.Background(), nil, logger, userID)
		if !d.Allowed {
			t.Fatalf("Allowed=false want true (fail-open)")
		}
		if !strings.Contains(logBuf.String(), "ratelimit_redis_failed") {
			t.Fatalf("logBuf missing WARN: %s", logBuf.String())
		}
	})
}
