package ledger

import (
	"context"
	"log/slog"

	"github.com/shopspring/decimal"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// Redis key schemes (data-models §4.3 + Story 5.2).
const (
	balanceRealtimeKeyPrefix = "balance:user:"
	balanceRealtimeKeySuffix = ":realtime"
	monthCostKeyPrefix       = "usage:apikey:"
	monthCostKeySuffix       = ":month_cost_usd"
)

// BalanceRealtimeKey is the fast-read mirror the hot-path 402 gate reads.
func BalanceRealtimeKey(userID string) string {
	return balanceRealtimeKeyPrefix + userID + balanceRealtimeKeySuffix
}

// MonthCostKey is the Story-5.2 per-api-key monthly cost counter (the 5.4
// cap-gate reads it; 7.1 is the writer).
func MonthCostKey(apiKeyID string) string {
	return monthCostKeyPrefix + apiKeyID + monthCostKeySuffix
}

// mirror runs the post-commit, best-effort cross-store fan-out (BR-D-3). Each
// step failure logs + increments he_billing_redis_mirror_failed_total and is
// otherwise swallowed — the durable PG charge already committed; the next
// deduction's reconcile re-syncs Redis (Q-RECON).
//
//   - SET balance:user:{id}:realtime = the post-deduction PG balance (Q-RECON
//     reconcile-from-PG, NOT a drift-prone DECRBYFLOAT).
//   - INCRBYFLOAT usage:apikey:{id}:month_cost_usd by cost (Story 5.2 writer).
//   - UPDATE api_keys.current_month_cost_usd (coarse NUMERIC(10,2) 5.4 input —
//     Architect M-2: NOT the reconciliation target).
func (l *Ledger) mirror(ctx context.Context, ev *billingv1.UsageEvent, costStr, newBalance string, cost decimal.Decimal) {
	if l.redis != nil {
		// Q-RECON — reconcile the realtime mirror from the authoritative PG value.
		if err := l.redis.Set(ctx, BalanceRealtimeKey(ev.GetUserId()), newBalance, 0).Err(); err != nil {
			l.mirrorFailed(ctx, "balance_realtime", ev, err)
		}
		// INCRBYFLOAT is inherently float in Redis; the counter is a cache (5.2),
		// not the billing SoT, so the bounded float drift is acceptable (M-2).
		if err := l.redis.IncrByFloat(ctx, MonthCostKey(ev.GetApiKeyId()), cost.InexactFloat64()).Err(); err != nil {
			l.mirrorFailed(ctx, "month_cost_counter", ev, err)
		}
	}

	// The 5.4 cap-gate input (PG, coarse 2-decimal). Best-effort — a failure here
	// leaves the cap counter slightly behind until the next deduction or the
	// monthly reset; it never affects the authoritative balance.
	if _, err := l.db.Exec(ctx, incApiKeyCostSQL, ev.GetApiKeyId(), costStr); err != nil {
		l.mirrorFailed(ctx, "api_key_month_cost", ev, err)
	}
}

func (l *Ledger) mirrorFailed(ctx context.Context, target string, ev *billingv1.UsageEvent, err error) {
	l.metrics.mirrorFailedInc(ctx, target)
	l.logger.WarnContext(ctx, "billing_redis_mirror_failed",
		slog.String("event", "billing_redis_mirror_failed"),
		slog.String("target", target),
		slog.String("he_request_id", ev.GetHeRequestId()),
		slog.String("error", err.Error()),
	)
}
