// Package credit is billing-svc's exactly-once balance CREDIT (Story 7.3, AC1/AC3)
// — the inverse of the 7.1 debit, with the same correctness invariants applied to
// the money-IN direction. It consumes `payment.completed` and applies it:
//
//   - EXACTLY-ONCE (BR-W-3): the credit applies ONLY on a recharge_orders
//     pending→paid transition (`UPDATE ... WHERE status='pending'`). A zero-row
//     update (redelivery, or a lost concurrent race) ⇒ skip the credit. The
//     status-flip and the balance-credit share ONE PG transaction (atomic).
//     UNIQUE(payment_provider, external_order_id) is the defence-in-depth fence.
//   - AMOUNT INTEGRITY (Q-AMOUNT / m2): the credited amount is the PROVIDER-
//     confirmed settled amount, never the client create-order intent. A settled ≠
//     intent mismatch leaves the order `pending` (NO flip, NO credit) and raises
//     a `he_payment_amount_mismatch_total` alert for manual disposition.
//   - USD SINGLE SoT (BR-R-2): the credit ALWAYS lands in balances.current_usd; a
//     non-USD settled amount is converted to USD at the 7.2 fx_rate at settlement
//     (Q-CURRENCY — built + unit-tested, though 7.3 enables USD only).
//   - PG-AUTHORITATIVE (BR-R-?): the Redis realtime mirror is written AFTER commit,
//     best-effort; a mirror failure never rolls back the durable PG credit.
//
// Subscriptions (RAIL only — Q-SUBSCOPE) update he_api.subscriptions status; they
// do NOT credit balances (a paid subscription marks status=active only; the
// entitlement mapping is Story 7.8). Because no money moves on a subscription
// event, a redelivery is harmless (it re-asserts the same terminal status).
package credit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// moneyScale is the NUMERIC(12,4) money scale.
const moneyScale int32 = 4

// balanceRealtimeKey mirrors the 7.1 ledger key scheme (the hot-path 402 gate
// reads it). Kept in sync with ledger.BalanceRealtimeKey.
func balanceRealtimeKey(userID string) string {
	return "balance:user:" + userID + ":realtime"
}

// Outcome classifies an Apply result so the consumer can ACK / retain.
type Outcome int

const (
	// OutcomeCredited — a recharge order was flipped pending→paid and balance credited.
	OutcomeCredited Outcome = iota
	// OutcomeDuplicate — the order was already paid (redelivery / lost race). No credit.
	OutcomeDuplicate
	// OutcomeMismatch — settled ≠ intent. Order stays pending, NO credit, alert raised.
	OutcomeMismatch
	// OutcomeOrderNotFound — the referenced order does not exist. ACK + skip.
	OutcomeOrderNotFound
	// OutcomeSubscription — a subscription lifecycle transition was applied (no credit).
	OutcomeSubscription
	// OutcomeIgnored — a non-actionable / failed / unknown event. ACK + skip.
	OutcomeIgnored
)

// DB is the pgx surface Apply needs: Begin (the credit tx) + QueryRow/Exec (the
// fx read + subscription update). Satisfied by *pgxpool.Pool + pgxmock.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Redis is the minimal go-redis surface the post-commit mirror needs.
type Redis interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
}

const (
	// readOrderSQL reads the pending order's intent. Used inside the credit tx.
	readOrderSQL = `SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders WHERE id = $1`

	// flipOrderSQL is the exactly-once fence: only a pending row flips to paid.
	// Zero rows ⇒ already paid (redelivery / lost race) ⇒ skip the credit.
	flipOrderSQL = `UPDATE he_api.recharge_orders
		SET status = 'paid', paid_at = NOW(), external_order_id = $2
	WHERE id = $1 AND status = 'pending'`

	// creditBalanceSQL lazily creates / credits the balance, RETURNING the new
	// value for the Redis reconcile (inverse of the 7.1 deduct UPSERT).
	creditBalanceSQL = `INSERT INTO he_api.balances AS b (user_id, current_usd, updated_at)
	VALUES ($1, $2::numeric, NOW())
	ON CONFLICT (user_id) DO UPDATE
		SET current_usd = b.current_usd + $2::numeric, updated_at = NOW()
	RETURNING current_usd::text`

	// fxRateSQL reads the latest USD→quote rate (7.2 Architect L-1: latest fetched_at).
	fxRateSQL = `SELECT rate::text FROM he_api.fx_rates
	WHERE base_currency = 'USD' AND quote_currency = $1
	ORDER BY fetched_at DESC LIMIT 1`

	// subscription lifecycle updates (RAIL only — no balance side-effect).
	subActivateSQL = `UPDATE he_api.subscriptions
		SET status = 'active', current_period_start = NOW(), current_period_end = NOW() + INTERVAL '30 days'
	WHERE payment_provider = $1 AND external_subscription_id = $2`
	subStatusSQL = `UPDATE he_api.subscriptions
		SET status = $3
	WHERE payment_provider = $1 AND external_subscription_id = $2`
)

// Applier consumes payment.completed and credits / updates durable storage.
type Applier struct {
	db     DB
	redis  Redis
	logger *slog.Logger

	credits      metric.Int64Counter     // he_billing_credit_total{result}
	amount       metric.Float64Histogram // he_billing_credit_amount_usd
	mismatch     metric.Int64Counter     // he_payment_amount_mismatch_total
	mirrorFailed metric.Int64Counter     // he_billing_credit_mirror_failed_total
}

// New builds an Applier. logger may be nil; redis may be nil (mirror skipped).
func New(db DB, rdb Redis, logger *slog.Logger) *Applier {
	if logger == nil {
		logger = slog.Default()
	}
	m := otel.Meter("apps/billing-svc/internal/credit")
	a := &Applier{db: db, redis: rdb, logger: logger}
	a.credits, _ = m.Int64Counter("he_billing_credit_total",
		metric.WithDescription("Balance credits by result (credited/duplicate/mismatch/order_not_found/subscription/ignored)"))
	a.amount, _ = m.Float64Histogram("he_billing_credit_amount_usd",
		metric.WithDescription("Per-recharge credited amount in USD"))
	a.mismatch, _ = m.Int64Counter("he_payment_amount_mismatch_total",
		metric.WithDescription("Settled-amount ≠ order-intent mismatches parked for manual disposition (Q-AMOUNT)"))
	a.mirrorFailed, _ = m.Int64Counter("he_billing_credit_mirror_failed_total",
		metric.WithDescription("Post-commit Redis realtime-mirror failures on the credit path"))
	return a
}

// Apply routes a payment.completed event. A returned error is a PG fault — the
// consumer does NOT ACK so Kafka redelivers (at-least-once + idempotent).
func (a *Applier) Apply(ctx context.Context, ev *paymentv1.PaymentEvent) (Outcome, error) {
	switch ev.GetEventType() {
	case "recharge_paid":
		return a.applyRecharge(ctx, ev)
	case "subscription_active", "subscription_renew":
		return a.applySubscription(ctx, ev, "active")
	case "subscription_past_due":
		return a.applySubscription(ctx, ev, "past_due")
	case "subscription_cancel":
		return a.applySubscription(ctx, ev, "cancelled")
	default:
		// recharge_failed / unknown — no money action. ACK + skip.
		a.count(ctx, "ignored")
		return OutcomeIgnored, nil
	}
}

// applyRecharge runs the exactly-once credit in one PG tx.
func (a *Applier) applyRecharge(ctx context.Context, ev *paymentv1.PaymentEvent) (Outcome, error) {
	orderID := ev.GetOrderId()
	if orderID == "" {
		a.count(ctx, "ignored")
		return OutcomeIgnored, nil
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("credit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID, intentStr, orderCurrency string
	if err := tx.QueryRow(ctx, readOrderSQL, orderID).Scan(&userID, &intentStr, &orderCurrency); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			a.count(ctx, "order_not_found")
			a.logger.WarnContext(ctx, "payment_credit_order_not_found",
				slog.String("event", "payment_credit_order_not_found"),
				slog.String("order_id", orderID),
			)
			return OutcomeOrderNotFound, nil // ACK — an unknown order is not retryable
		}
		return 0, fmt.Errorf("credit: read order: %w", err)
	}

	// Amount integrity (Q-AMOUNT / m2 + ROBUST-003 + Q-FX-INTENT, Story 7.5): the
	// provider-confirmed settled amount must match the order intent. Compare in the
	// PAID currency — settled vs intent, BOTH pre-conversion, AND the same currency —
	// so fx-rate drift between order-create and settlement does NOT falsely trip the
	// park (Q-FX-INTENT). A settled≠intent mismatch, a currency mismatch, OR an
	// unparseable/corrupt stored intent (ierr) parks the order (no flip, no credit) +
	// alerts, rather than crediting unchecked.
	//
	// ⚠️ Story 7.5 (Alipay+) is the FIRST channel to settle a native non-USD currency
	// (CNY). Prior to this the guard compared the USD-converted credit against the raw
	// paid-currency intent, which silently parked every CNY recharge (50.0000 USD ≠
	// 350.00 CNY). Comparing in the paid currency BEFORE conversion fixes that while
	// leaving the USD path byte-identical (USD: settled == intent == creditUSD).
	settled, serr := decimal.NewFromString(strings.TrimSpace(ev.GetSettledAmount()))
	intent, ierr := decimal.NewFromString(intentStr)
	settledCur := strings.ToUpper(strings.TrimSpace(ev.GetCurrency()))
	intentCur := strings.ToUpper(strings.TrimSpace(orderCurrency))
	if serr != nil || ierr != nil || settledCur != intentCur || !settled.Equal(intent) {
		a.mismatch.Add(ctx, 1)
		a.count(ctx, "mismatch")
		a.logger.WarnContext(ctx, "payment_amount_mismatch",
			slog.String("event", "payment_amount_mismatch"),
			slog.String("order_id", orderID),
			slog.String("settled", strings.TrimSpace(ev.GetSettledAmount())),
			slog.String("settled_currency", settledCur),
			slog.String("intent", intentStr), // raw stored intent (may be unparseable)
			slog.String("intent_currency", intentCur),
		)
		// Commit nothing (the deferred rollback discards the read). Order stays pending.
		return OutcomeMismatch, nil
	}

	// Integrity passed — NOW convert the verified settled amount to USD for the credit
	// (Q-CURRENCY / BR-C-4). For a USD settlement this is a no-op pass-through; a CNY
	// settlement converts at the latest 7.2 fx_rate (HALF-UP, moneyScale).
	creditUSD, err := a.toUSD(ctx, ev.GetSettledAmount(), ev.GetCurrency())
	if err != nil {
		return 0, fmt.Errorf("credit: convert amount: %w", err)
	}

	// Exactly-once fence: only a pending row flips. Zero rows ⇒ redelivery / race.
	tag, err := tx.Exec(ctx, flipOrderSQL, orderID, ev.GetExternalOrderId())
	if err != nil {
		return 0, fmt.Errorf("credit: flip order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		a.count(ctx, "duplicate")
		return OutcomeDuplicate, nil // already paid — no second credit (BR-W-3)
	}

	creditStr := creditUSD.StringFixed(moneyScale)
	var newBalance string
	if err := tx.QueryRow(ctx, creditBalanceSQL, userID, creditStr).Scan(&newBalance); err != nil {
		return 0, fmt.Errorf("credit: apply balance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("credit: commit: %w", err)
	}

	a.count(ctx, "credited")
	a.amount.Record(ctx, creditUSD.InexactFloat64())
	a.logger.InfoContext(ctx, "payment_credit",
		slog.String("event", "payment_credit"),
		slog.String("order_id", orderID),
		slog.String("user_id", userID),
		slog.String("provider", ev.GetPaymentProvider()),
		slog.String("settled_amount", creditStr),
	)

	// Post-commit best-effort mirror (inverse of the 7.1 debit mirror).
	if a.redis != nil {
		if err := a.redis.Set(ctx, balanceRealtimeKey(userID), newBalance, 0).Err(); err != nil {
			a.mirrorFailed.Add(ctx, 1)
			a.logger.WarnContext(ctx, "payment_credit_mirror_failed",
				slog.String("event", "payment_credit_mirror_failed"),
				slog.String("user_id", userID),
				slog.String("error", err.Error()),
			)
		}
	}
	return OutcomeCredited, nil
}

// applySubscription updates a subscription's lifecycle status (RAIL only — no
// balance credit, Q-SUBSCOPE). Idempotent: re-applying yields the same terminal
// status; because no money moves, a redelivery is harmless.
func (a *Applier) applySubscription(ctx context.Context, ev *paymentv1.PaymentEvent, status string) (Outcome, error) {
	extID := strings.TrimSpace(ev.GetExternalSubscriptionId())
	if extID == "" {
		a.count(ctx, "ignored")
		return OutcomeIgnored, nil
	}
	var err error
	if status == "active" {
		_, err = a.db.Exec(ctx, subActivateSQL, ev.GetPaymentProvider(), extID)
	} else {
		_, err = a.db.Exec(ctx, subStatusSQL, ev.GetPaymentProvider(), extID, status)
	}
	if err != nil {
		return 0, fmt.Errorf("credit: subscription update: %w", err)
	}
	a.count(ctx, "subscription")
	a.logger.InfoContext(ctx, "payment_subscription_update",
		slog.String("event", "payment_subscription_update"),
		slog.String("external_subscription_id", extID),
		slog.String("status", status),
	)
	return OutcomeSubscription, nil
}

// toUSD converts a string-decimal settled amount in `currency` to USD. USD passes
// through; a non-USD amount divides by the latest USD→currency fx rate (HALF-UP,
// 4dp). Q-CURRENCY: built + unit-tested though 7.3 enables USD only.
func (a *Applier) toUSD(ctx context.Context, amount, currency string) (decimal.Decimal, error) {
	amt, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return decimal.Zero, fmt.Errorf("bad settled amount %q", amount)
	}
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" || cur == "USD" {
		return amt.Round(moneyScale), nil
	}
	var rateStr string
	if err := a.db.QueryRow(ctx, fxRateSQL, cur).Scan(&rateStr); err != nil {
		return decimal.Zero, fmt.Errorf("fx rate for %s: %w", cur, err)
	}
	rate, err := decimal.NewFromString(rateStr)
	if err != nil || !rate.IsPositive() {
		return decimal.Zero, fmt.Errorf("bad fx rate %q", rateStr)
	}
	// rate is quote-per-USD (e.g. CNY per USD); USD = amount / rate.
	return amt.DivRound(rate, moneyScale), nil
}

func (a *Applier) count(ctx context.Context, result string) {
	if a == nil || a.credits == nil {
		return
	}
	a.credits.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
