// Story 7.1 (AC3 / T3.3) — Redis-backed realtime balance READ contract for the
// pre-flight 402 gate.
//
// The realtime per-user balance mirror is written by billing-svc (Q-RECON:
// reconciled from the authoritative PG value on each deduction):
//
//	balance:user:{user_id}:realtime   (SET by billing-svc; GET by the gateway
//	                                    hot-path 402 gate; no TTL)
//
// The gate reads this fast mirror — NOT the PG-authoritative balances table —
// because speed > strict accuracy on the hot path, and it is FAIL-OPEN: an
// absent key / transport error allows the request (BR-A-4).
package usage

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// BalanceRealtimeKeyPrefix + suffix compose the realtime balance mirror key
// (data-models §4.3). MUST match billing-svc internal/ledger BalanceRealtimeKey.
const (
	BalanceRealtimeKeyPrefix = "balance:user:"
	BalanceRealtimeKeySuffix = ":realtime"
)

// BalanceRealtimeKey returns the realtime balance mirror key for a user_id.
func BalanceRealtimeKey(userID string) string {
	return BalanceRealtimeKeyPrefix + userID + BalanceRealtimeKeySuffix
}

// ReadRealtimeBalance GETs the realtime balance mirror as a string-decimal:
//
//	(value, true, nil)  — mirror present
//	("",    false, nil) — mirror absent (new/not-yet-deducted user → gate allows)
//	("",    false, err) — Redis transport error (gate fail-OPEN per BR-A-4)
//
// A nil client returns ("", false, nil) — Redis-disabled test environments
// degrade to "no mirror" (gate always allows).
func ReadRealtimeBalance(ctx context.Context, rdb *redis.Client, userID string) (string, bool, error) {
	if rdb == nil {
		return "", false, nil
	}
	val, err := rdb.Get(ctx, BalanceRealtimeKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read realtime balance: %w", err)
	}
	return val, true, nil
}
