// Story 5.2 T5.1 — Redis-backed monthly-cost counter READ contract.
//
// The realtime per-key monthly spend counter is the Q-D Redis SoT:
//
//	usage:apikey:{api_key_id}:month_cost_usd   (INCRBYFLOAT by billing-svc;
//	                                             GET by the gateway hot path;
//	                                             no TTL — cron-reset by 5.4)
//
// Story 5.2 ships only the READ side; the WRITE side is billing-svc (Epic
// 6+). Until billing-svc lands the key is absent → ReadMonthlyCostUSD returns
// ("", false, nil) and the cap check treats it as 0 (BR-4.4).
package usage

import (
	"context"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// CounterKeyPrefix + suffix compose the Q-D Redis SoT key. Documented in
// data-models.md §4.3 (Story 5.2 row).
const (
	CounterKeyPrefix = "usage:apikey:"
	CounterKeySuffix = ":month_cost_usd"
)

// CounterKey returns the realtime counter key for an api_key_id.
func CounterKey(apiKeyID string) string {
	return CounterKeyPrefix + apiKeyID + CounterKeySuffix
}

// ReadMonthlyCostUSD GETs the realtime counter as a string-decimal. Returns:
//
//	(value, true, nil)  — counter present
//	("",    false, nil) — counter absent (BR-4.4 → caller treats as 0)
//	("",    false, err) — Redis transport error (caller fail-OPEN per Q-F)
//
// A nil client returns ("", false, nil) — Redis intentionally disabled in
// some test environments degrades to "no counter" (always below cap).
func ReadMonthlyCostUSD(ctx context.Context, rdb *redis.Client, apiKeyID string) (string, bool, error) {
	if rdb == nil {
		return "", false, nil
	}
	val, err := rdb.Get(ctx, CounterKey(apiKeyID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read monthly cost: %w", err)
	}
	return val, true, nil
}
