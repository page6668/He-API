// Story 5.4 — sticky-trip cap-tripped sentinel state + threshold-crossing
// helpers layered onto the Story-5.2 monthly-cap gate.
//
// The sentinel families live in the `keystate:apikey:*` namespace (TC-2),
// distinct from the Story-5.1 `auth:apikey:*` cache-invalidation namespace.
// All three are no-TTL (Q-A) — the monthly-cost-reset CronJob (AC3) is the
// single mutation surface that clears them at the UTC month boundary, so the
// prefix constants are mirrored in
// apps/auth-svc/internal/redisclient/cap_state.go for the cron's SCAN.
package keypolicy

import (
	"context"
	"math/big"

	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/notifyclient"
)

// Redis key prefixes (TC-2 / T0.3). Keep in lockstep with the auth-svc cron's
// SCAN prefixes (apps/auth-svc/internal/redisclient/cap_state.go).
const (
	// CapTrippedSentinelPrefix — sticky-trip breaker (no TTL). Presence ⇒ 402.
	CapTrippedSentinelPrefix = "keystate:apikey:cap_tripped:"
	// CapWarning80NotifiedPrefix — once-per-month 80%-warning email dedupe.
	CapWarning80NotifiedPrefix = "keystate:apikey:cap_warning_80_notified:"
	// CapTrippedNotifiedPrefix — once-per-month tripped-email dedupe.
	CapTrippedNotifiedPrefix = "keystate:apikey:cap_tripped_notified:"
)

// Threshold ratios (BR-2.1 / m-2). Named so future product-driven
// configurability is a one-rename change rather than a literal hunt. The exact
// big.Rat comparison in crossedWarning uses 80/100 to avoid float64 precision
// loss (BR-4.5); these constants are the documented source-of-truth + the
// value 5.4-UNIT-012 asserts.
const (
	MonthlyCapWarningThresholdRatio = 0.80 // WARNING_80 detection trigger
	MonthlyCapTrippedThresholdRatio = 1.00 // TRIPPED detection trigger (>= cap)
)

// CapTrippedSentinel reads and claims the sticky-trip breaker. Exists is the
// AC1 BR-1.1 fast-path probe; SetNX is the BR-1.9 first-cross dedup claim
// (true ⇒ this caller won the race and owns the tripped-email fire).
type CapTrippedSentinel interface {
	Exists(ctx context.Context, apiKeyID string) (bool, error)
	SetNX(ctx context.Context, apiKeyID string) (bool, error)
}

// CapThresholdNotifier fires the fire-and-forget threshold notification
// (satisfied by *notifyclient.Client).
type CapThresholdNotifier interface {
	NotifyCapThresholdAsync(ctx context.Context, apiKeyID string, threshold notifyclient.Threshold)
}

// RedisCapSentinel implements CapTrippedSentinel over go-redis. The value is
// the constant "1"; TTL is 0 (no expiry) per Q-A.
type RedisCapSentinel struct{ rdb *redis.Client }

// NewRedisCapSentinel wires the sticky-trip sentinel over an existing client.
func NewRedisCapSentinel(rdb *redis.Client) *RedisCapSentinel {
	return &RedisCapSentinel{rdb: rdb}
}

// Exists reports whether the sticky-trip sentinel is set (BR-1.1).
func (s *RedisCapSentinel) Exists(ctx context.Context, apiKeyID string) (bool, error) {
	n, err := s.rdb.Exists(ctx, CapTrippedSentinelPrefix+apiKeyID).Result()
	return n > 0, err
}

// SetNX claims the sticky-trip sentinel; returns true only for the first
// writer (BR-1.9). No TTL (Q-A — cron-cleared).
func (s *RedisCapSentinel) SetNX(ctx context.Context, apiKeyID string) (bool, error) {
	return s.rdb.SetNX(ctx, CapTrippedSentinelPrefix+apiKeyID, "1", 0).Result()
}

// crossedWarning reports current/cap >= MonthlyCapWarningThresholdRatio using
// exact decimal arithmetic (big.Rat — no float precision loss, BR-4.5). A
// missing/empty counter is treated as 0; a non-positive or unparseable cap
// returns false (no warning — fail-safe, the cap gate itself fails-open).
func crossedWarning(currentUSD, capUSD string) bool {
	capRat, ok := new(big.Rat).SetString(capUSD)
	if !ok || capRat.Sign() <= 0 {
		return false
	}
	cur := new(big.Rat)
	if currentUSD != "" {
		if parsed, ok := new(big.Rat).SetString(currentUSD); ok {
			cur = parsed
		}
	}
	// current >= 0.80 * cap  (== MonthlyCapWarningThresholdRatio).
	threshold := new(big.Rat).Mul(capRat, big.NewRat(80, 100))
	return cur.Cmp(threshold) >= 0
}
