// Package autorecharge is billing-svc's Story-7.7 AC1 trigger: after a 7.1
// deduction COMMITS (off the chat hot path — never inside the deduction tx,
// BR-R-1), it evaluates whether the new balance fell below the user's
// auto-recharge threshold and, if so, starts AT MOST ONE off-session top-up.
//
// The at-most-one-in-flight invariant (BR-R-2, the dominant correctness risk) is a
// TWO-LAYER fence (Architect Q-TRIGGER ruling):
//
//   - FAST GATE: a Redis SETNX lock `autorecharge:lock:user:{id}` (TTL-bounded by
//     the charge + webhook-settle window). Concurrent debits race the SETNX;
//     exactly one wins.
//   - DURABLE FENCE: a PARTIAL UNIQUE index on recharge_orders (user_id) WHERE
//     status='pending' AND is_auto_recharge. This SURVIVES Redis loss: even with
//     the lock unavailable, a second pending auto-order insert fails with a unique
//     violation → fence-held → no double top-up.
//
// Both are load-bearing (defense in depth) — the Redis lock alone cannot cover a
// Redis-less window before the first pending row commits.
//
// The CREDIT is NOT a new path: the off-session charge settles via the 7.3
// `payment.completed` webhook → the 7.3 exactly-once credit applier (UNCHANGED).
package autorecharge

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
)

// DefaultFailLimit is N for Q-FAILMODE auto-disable: after this many consecutive
// failed off-session charges, auto_recharge_enabled is flipped FALSE.
const DefaultFailLimit = 3

// DefaultLockTTL bounds a stuck charge (the fail-safe release; the primary release
// is the order reaching a terminal state, which clears the partial-unique fence).
const DefaultLockTTL = 5 * time.Minute

// Outcome classifies an Evaluate result.
type Outcome int

const (
	// OutcomeNoAction — not eligible (disabled or balance ≥ threshold) or no fence work.
	OutcomeNoAction Outcome = iota
	// OutcomeTriggered — a top-up was started (one pending auto recharge_order).
	OutcomeTriggered
	// OutcomeFenceHeld — an auto-recharge is already in flight; this observer no-ops.
	OutcomeFenceHeld
	// OutcomeFailed — the off-session charge declined; order=failed, fence released.
	OutcomeFailed
	// OutcomeDisabled — N consecutive failures → auto_recharge_enabled set FALSE.
	OutcomeDisabled
)

// AlertKind tells the caller (T3) which low-balance email, if any, to dispatch.
type AlertKind int

const (
	AlertNone       AlertKind = iota // suppressed (auto-recharge succeeded, or above threshold)
	AlertLowBalance                  // 余额预警 — auto-recharge OFF/unset and below threshold
	AlertFailed                      // auto-recharge ON but the charge failed
)

// Result is what Evaluate returns to the post-deduction caller.
type Result struct {
	Outcome   Outcome
	Alert     AlertKind
	Balance   string // string-decimal current balance (for the alert payload)
	Threshold string // the resolved alert threshold (for the alert payload)
}

// DB is the minimal pgx surface the trigger needs.
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Locker is the minimal go-redis surface for the fast-gate lock + fail counter.
// Satisfied by *redis.Client (production + miniredis tests).
type Locker interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Incr(ctx context.Context, key string) *redis.IntCmd
}

// TokenSource resolves an owned method's secret-grade token (satisfied by
// *paymentmethod.Store). The (id, user_id) scoping is the cross-user-binding guard.
type TokenSource interface {
	GetOwnedToken(ctx context.Context, userID, methodID string) (token, provider string, found bool, err error)
}

// Charger calls payment-svc's off-session charge. orderID is OUR pending
// recharge_orders.id (carried into Stripe metadata so the 7.3 webhook resolves
// it). status is "pending" (processing → webhook will settle into the 7.3 credit)
// or "failed" (declined).
type Charger interface {
	ChargeOffSession(ctx context.Context, userID, orderID, pmToken, amount, currency string) (extOrderID, status string, err error)
}

const (
	readConfigSQL = `SELECT COALESCE(auto_recharge_enabled, FALSE),
		COALESCE(auto_recharge_threshold_usd::text, ''),
		COALESCE(auto_recharge_amount_usd::text, ''),
		COALESCE(auto_recharge_payment_method_id::text, '')
	FROM he_api.balances WHERE user_id = $1`

	// Durable fence: the partial UNIQUE index rejects a 2nd concurrent pending auto.
	insertAutoOrderSQL = `INSERT INTO he_api.recharge_orders
		(user_id, amount, currency, payment_provider, status, is_auto_recharge, created_at)
	VALUES ($1, $2::numeric, 'USD', $3, 'pending', TRUE, NOW())
	RETURNING id::text`

	markFailedSQL = `UPDATE he_api.recharge_orders SET status = 'failed' WHERE id = $1`

	disableSQL = `UPDATE he_api.balances SET auto_recharge_enabled = FALSE WHERE user_id = $1`
)

func lockKey(userID string) string { return "autorecharge:lock:user:" + userID }
func failKey(userID string) string { return "autorecharge:fail:user:" + userID }

// Trigger evaluates and acts on the auto-recharge condition. Construct once.
type Trigger struct {
	db        DB
	redis     Locker
	tokens    TokenSource
	charger   Charger
	logger    *slog.Logger
	lockTTL   time.Duration
	failLimit int64
	// alertDefault is the env-driven system low-balance threshold used when
	// auto-recharge is OFF/unset (Q-ALERT-THRESHOLD); empty disables the alert.
	alertDefault string
}

// New builds a Trigger. redis/tokens/charger may be nil (the trigger degrades to
// no-op auto-recharge — the deduction is already durable). alertDefault is the
// HE_API_LOW_BALANCE_THRESHOLD_USD env value (may be empty).
func New(db DB, rdb Locker, tokens TokenSource, charger Charger, alertDefault string, logger *slog.Logger) *Trigger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Trigger{
		db: db, redis: rdb, tokens: tokens, charger: charger, logger: logger,
		lockTTL: DefaultLockTTL, failLimit: DefaultFailLimit,
		alertDefault: strings.TrimSpace(alertDefault),
	}
}

// Evaluate runs the post-deduction auto-recharge + alert decision for userID given
// the post-deduction balance. It never returns an error that should roll back the
// (already-committed) deduction — failures are logged and surfaced as outcomes.
func (t *Trigger) Evaluate(ctx context.Context, userID, balanceStr string) Result {
	userID = strings.TrimSpace(userID)
	bal, berr := decimal.NewFromString(strings.TrimSpace(balanceStr))
	if userID == "" || berr != nil {
		return Result{Outcome: OutcomeNoAction, Alert: AlertNone}
	}

	enabled, thrStr, amtStr, pmID, err := t.readConfig(ctx, userID)
	if err != nil {
		t.logger.WarnContext(ctx, "autorecharge_config_read_failed",
			slog.String("event", "autorecharge_config_read_failed"), slog.String("error", err.Error()))
		return Result{Outcome: OutcomeNoAction, Alert: AlertNone}
	}

	// Resolve the alert threshold (Q-ALERT-THRESHOLD): the auto-recharge threshold
	// when set, else the env system default. Empty → no alert threshold.
	alertThr := thrStr
	if alertThr == "" {
		alertThr = t.alertDefault
	}
	below := belowThreshold(bal, alertThr)

	// Auto-recharge trigger path: enabled + a configured threshold + a method + below.
	thr, terr := decimal.NewFromString(thrStr)
	if enabled && pmID != "" && terr == nil && bal.LessThan(thr) {
		return t.trigger(ctx, userID, amtStr, pmID, bal, alertThr)
	}

	// Not auto-recharging → fall back to the low-balance alert (T3) when below.
	if below {
		return Result{Outcome: OutcomeNoAction, Alert: AlertLowBalance, Balance: bal.StringFixed(4), Threshold: alertThr}
	}
	return Result{Outcome: OutcomeNoAction, Alert: AlertNone, Balance: bal.StringFixed(4)}
}

func (t *Trigger) trigger(ctx context.Context, userID, amtStr, pmID string, bal decimal.Decimal, alertThr string) Result {
	res := Result{Balance: bal.StringFixed(4), Threshold: alertThr}
	if t.charger == nil || t.tokens == nil {
		res.Outcome = OutcomeNoAction
		return res
	}
	amt, aerr := decimal.NewFromString(strings.TrimSpace(amtStr))
	if aerr != nil || !amt.IsPositive() {
		res.Outcome = OutcomeNoAction
		return res
	}

	// FAST GATE — Redis SETNX. A nil redis degrades to the durable fence alone.
	locked := true
	if t.redis != nil {
		ok, lerr := t.redis.SetNX(ctx, lockKey(userID), "1", t.lockTTL).Result()
		if lerr == nil && !ok {
			res.Outcome = OutcomeFenceHeld
			return res
		}
		locked = lerr == nil
	}

	// DURABLE FENCE — the partial-unique pending-auto insert. A unique violation
	// means an auto-recharge is already in flight (survives Redis loss).
	resolveToken := func() (string, string, bool, error) { return t.tokens.GetOwnedToken(ctx, userID, pmID) }
	token, provider, found, terr := resolveToken()
	if terr != nil || !found {
		t.releaseLock(ctx, userID)
		res.Outcome = OutcomeNoAction
		return res
	}

	var orderID string
	ierr := t.db.QueryRow(ctx, insertAutoOrderSQL, userID, amt.StringFixed(4), provider).Scan(&orderID)
	if ierr != nil {
		if isUniqueViolation(ierr) {
			res.Outcome = OutcomeFenceHeld
			return res
		}
		t.releaseLock(ctx, userID)
		t.logger.ErrorContext(ctx, "autorecharge_insert_order_failed",
			slog.String("event", "autorecharge_insert_order_failed"), slog.String("error", ierr.Error()))
		res.Outcome = OutcomeNoAction
		return res
	}
	_ = locked

	// Charge off-session. The CREDIT, if it settles, lands via the 7.3 webhook.
	_, status, cerr := t.charger.ChargeOffSession(ctx, userID, orderID, token, amt.StringFixed(2), "USD")
	if cerr != nil || status == "failed" {
		return t.onChargeFailure(ctx, userID, orderID, bal, alertThr, cerr)
	}

	// Success path: order stays pending; the webhook flips it paid + credits.
	// On a successful recharge the user gets the 7.3 receipt — NOT a low-balance
	// alert (suppression, BR-A-2). Reset the consecutive-failure counter.
	if t.redis != nil {
		_ = t.redis.Del(ctx, failKey(userID)).Err()
	}
	t.logger.InfoContext(ctx, "autorecharge_triggered",
		slog.String("event", "autorecharge_triggered"), slog.String("user_id", userID),
		slog.String("order_id", orderID), slog.String("amount_usd", amt.StringFixed(2)))
	res.Outcome = OutcomeTriggered
	res.Alert = AlertNone
	return res
}

func (t *Trigger) onChargeFailure(ctx context.Context, userID, orderID string, bal decimal.Decimal, alertThr string, cerr error) Result {
	if _, err := t.db.Exec(ctx, markFailedSQL, orderID); err != nil {
		t.logger.ErrorContext(ctx, "autorecharge_mark_failed_failed",
			slog.String("event", "autorecharge_mark_failed_failed"), slog.String("error", err.Error()))
	}
	t.releaseLock(ctx, userID)

	res := Result{Outcome: OutcomeFailed, Alert: AlertFailed, Balance: bal.StringFixed(4), Threshold: alertThr}

	// Q-FAILMODE: count consecutive failures; auto-disable at the limit.
	if t.redis != nil {
		n, ierr := t.redis.Incr(ctx, failKey(userID)).Result()
		if ierr == nil && n >= t.failLimit {
			if _, err := t.db.Exec(ctx, disableSQL, userID); err == nil {
				_ = t.redis.Del(ctx, failKey(userID)).Err()
				res.Outcome = OutcomeDisabled
				t.logger.WarnContext(ctx, "autorecharge_auto_disabled",
					slog.String("event", "autorecharge_auto_disabled"), slog.String("user_id", userID),
					slog.Int64("consecutive_failures", n))
			}
		}
	}
	t.logger.WarnContext(ctx, "autorecharge_charge_failed",
		slog.String("event", "autorecharge_charge_failed"), slog.String("user_id", userID),
		slog.Bool("had_error", cerr != nil))
	return res
}

func (t *Trigger) releaseLock(ctx context.Context, userID string) {
	if t.redis != nil {
		_ = t.redis.Del(ctx, lockKey(userID)).Err()
	}
}

func (t *Trigger) readConfig(ctx context.Context, userID string) (enabled bool, threshold, amount, pmID string, err error) {
	err = t.db.QueryRow(ctx, readConfigSQL, userID).Scan(&enabled, &threshold, &amount, &pmID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", "", "", nil
	}
	return enabled, threshold, amount, pmID, err
}

// belowThreshold reports whether bal < threshold (threshold parsed from a
// string-decimal; an empty/invalid threshold means "no alert threshold" → false).
func belowThreshold(bal decimal.Decimal, thresholdStr string) bool {
	thr, err := decimal.NewFromString(strings.TrimSpace(thresholdStr))
	if err != nil {
		return false
	}
	return bal.LessThan(thr)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
