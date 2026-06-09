// Package ledger is billing-svc's exactly-once async deduction (Story 7.1 AC2).
// It applies a usage.recorded event to durable storage with three invariants:
//
//   - EXACTLY-ONCE (BR-D-1): usage_ledger.ledger_key PRIMARY KEY + INSERT ...
//     ON CONFLICT DO NOTHING. A Kafka redelivery inserts zero rows → the event
//     is ACKed and skipped, never double-charged.
//   - ATOMICITY (BR-D-2): the ledger-insert and the balance-UPDATE share ONE PG
//     transaction. A crash mid-tx rolls back BOTH (no orphan row, no phantom
//     charge). The Kafka offset is committed by the consumer only AFTER this tx
//     commits.
//   - PG-AUTHORITATIVE (BR-D-3 / Q-RECON): the Redis realtime mirror + month
//     counters are written AFTER commit, best-effort. A mirror failure logs +
//     increments he_billing_redis_mirror_failed_total but never rolls back the
//     durable PG charge (the next deduction's reconcile re-syncs Redis).
package ledger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/billing-svc/internal/pricing"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// Outcome classifies an Apply result so the consumer can decide ACK vs DLQ vs
// offset-retain.
type Outcome int

const (
	// OutcomeApplied — the deduction was applied (one new ledger row + balance debit).
	OutcomeApplied Outcome = iota
	// OutcomeDuplicate — the ledger_key already existed (Kafka redelivery). No
	// charge applied; the consumer ACKs (exactly-once, BR-D-1).
	OutcomeDuplicate
	// OutcomeNoPricing — the model has no pricing row (ErrNoPricing). No charge;
	// the consumer dead-letters the event (BR-D-8 — never silently zero-charge).
	OutcomeNoPricing
)

// Result is the outcome of Apply plus the computed cost (zero for Duplicate /
// NoPricing).
type Result struct {
	Outcome Outcome
	Cost    decimal.Decimal
}

// DB is the minimal pgx surface Apply needs: Begin (the deduction tx) + Exec
// (the post-commit best-effort api_keys counter). Satisfied by *pgxpool.Pool and
// pgxmock.PgxPoolIface.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Pricer supplies the current immutable cost snapshot (satisfied by
// *pricing.Provider; tests inject a stub).
type Pricer interface {
	Current() *pricing.Snapshot
}

// Redis is the minimal go-redis surface the post-commit fan-out needs.
// Satisfied directly by *redis.Client (production + miniredis tests).
type Redis interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	IncrByFloat(ctx context.Context, key string, value float64) *redis.FloatCmd
}

const (
	insertLedgerSQL = `INSERT INTO he_api.usage_ledger
	(ledger_key, he_request_id, user_id, api_key_id, model, prompt_tokens, completion_tokens, cost_usd, billing_mode, ts, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, COALESCE($10::timestamptz, NOW()), NOW())
ON CONFLICT (ledger_key) DO NOTHING`

	// Lazy UPSERT (Q-LAZY): create the balances row at -cost on first deduction;
	// else subtract. RETURNING the post-deduction value drives the Q-RECON Redis
	// reconcile (SET-from-PG, NOT a drift-prone DECRBYFLOAT).
	deductBalanceSQL = `INSERT INTO he_api.balances AS b (user_id, current_usd, updated_at)
VALUES ($1, (0 - $2::numeric), NOW())
ON CONFLICT (user_id) DO UPDATE
	SET current_usd = b.current_usd - $2::numeric, updated_at = NOW()
RETURNING current_usd::text`

	// Post-commit, best-effort. The coarse NUMERIC(10,2) cap counter (Story 5.4
	// input) — Architect M-2: this is NOT the reconciliation target, bounded
	// sub-cent drift is acceptable and reset monthly by the 5.4 cron.
	incApiKeyCostSQL = `UPDATE he_api.api_keys
	SET current_month_cost_usd = current_month_cost_usd + $2::numeric
WHERE id = $1`
)

// Ledger applies usage events. Construct once and reuse (concurrency-safe — it
// holds no per-event state).
type Ledger struct {
	db      DB
	redis   Redis
	pricer  Pricer
	logger  *slog.Logger
	metrics *Metrics
}

// New builds a Ledger. logger may be nil (slog.Default()); redis may be nil
// (the post-commit mirror is skipped — PG charge still durable).
func New(db DB, redis Redis, pricer Pricer, logger *slog.Logger, m *Metrics) *Ledger {
	if logger == nil {
		logger = slog.Default()
	}
	if m == nil {
		m = NewMetrics()
	}
	return &Ledger{db: db, redis: redis, pricer: pricer, logger: logger, metrics: m}
}

// Apply executes the exactly-once deduction for one usage event.
//
// Returns:
//   - (Applied, cost), nil  — deducted; consumer ACKs.
//   - (Duplicate, 0), nil   — redelivery; consumer ACKs (BR-D-1).
//   - (NoPricing, 0), nil   — no pricing row; consumer DLQs (BR-D-8).
//   - (_, _), err           — a PG fault; consumer does NOT ACK → Kafka
//     redelivers when PG recovers (at-least-once + idempotent, BR-D-2).
func (l *Ledger) Apply(ctx context.Context, ev *billingv1.UsageEvent) (Result, error) {
	// Cost is computed BEFORE the tx (pure, no I/O). ErrNoPricing → DLQ without
	// touching PG (no ledger row, no charge — BR-D-8).
	res, err := l.pricer.Current().ComputeCost(ev.GetModel(), ev.GetPromptTokens(), ev.GetCompletionTokens(), ev.GetBillingMode())
	if err != nil {
		if errors.Is(err, pricing.ErrNoPricing) {
			l.metrics.deductionInc(ctx, "no_pricing")
			l.logger.WarnContext(ctx, "billing_no_pricing",
				slog.String("event", "billing_no_pricing"),
				slog.String("he_request_id", ev.GetHeRequestId()),
				slog.String("model", ev.GetModel()),
			)
			return Result{Outcome: OutcomeNoPricing}, nil
		}
		l.metrics.deductionInc(ctx, "error")
		return Result{}, fmt.Errorf("compute cost: %w", err)
	}
	cost := res.Cost
	costStr := cost.StringFixed(pricing.CostScale)

	if res.MarkupOutOfRange {
		// Q-ROUND out-of-range — bill as-is, but WARN (never silently drop).
		l.logger.WarnContext(ctx, "billing_markup_out_of_range",
			slog.String("event", "billing_markup_out_of_range"),
			slog.String("model", ev.GetModel()),
			slog.String("he_request_id", ev.GetHeRequestId()),
		)
	}

	newBalance, outcome, err := l.deduct(ctx, ev, costStr)
	if err != nil {
		l.metrics.deductionInc(ctx, "error")
		return Result{}, err
	}
	if outcome == OutcomeDuplicate {
		l.metrics.deductionInc(ctx, "duplicate")
		return Result{Outcome: OutcomeDuplicate}, nil
	}

	l.metrics.deductionInc(ctx, "applied")
	l.metrics.observeAmount(ctx, cost)
	l.logger.InfoContext(ctx, "billing_deduct",
		slog.String("event", "billing_deduct"),
		slog.String("he_request_id", ev.GetHeRequestId()),
		slog.String("user_id", ev.GetUserId()),
		slog.String("model", ev.GetModel()),
		slog.String("cost_usd", costStr),
	)

	// Post-commit best-effort fan-out (BR-D-3): never rolls back the PG charge.
	l.mirror(ctx, ev, costStr, newBalance, cost)

	return Result{Outcome: OutcomeApplied, Cost: cost}, nil
}

// deduct runs the atomic ledger-insert + balance-UPDATE in one tx. Returns the
// post-deduction balance (for the Redis reconcile) and the outcome.
func (l *Ledger) deduct(ctx context.Context, ev *billingv1.UsageEvent, costStr string) (newBalance string, outcome Outcome, err error) {
	tx, err := l.db.Begin(ctx)
	if err != nil {
		return "", OutcomeApplied, fmt.Errorf("begin tx: %w", err)
	}
	// Safety-net rollback; a no-op after a successful Commit (pgx contract).
	defer func() { _ = tx.Rollback(ctx) }()

	var ts *string
	if t := ev.GetTs(); t != "" {
		ts = &t
	}
	tag, err := tx.Exec(ctx, insertLedgerSQL,
		ledgerKey(ev), ev.GetHeRequestId(), ev.GetUserId(), ev.GetApiKeyId(),
		ev.GetModel(), int64(ev.GetPromptTokens()), int64(ev.GetCompletionTokens()),
		costStr, billingModeString(ev.GetBillingMode()), ts,
	)
	if err != nil {
		return "", OutcomeApplied, fmt.Errorf("insert ledger: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Redelivery — the ledger_key already exists. Skip the deduction; the
		// defer rolls back (nothing to commit). Exactly-once (BR-D-1).
		return "", OutcomeDuplicate, nil
	}

	if err := tx.QueryRow(ctx, deductBalanceSQL, ev.GetUserId(), costStr).Scan(&newBalance); err != nil {
		return "", OutcomeApplied, fmt.Errorf("deduct balance: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", OutcomeApplied, fmt.Errorf("commit: %w", err)
	}
	return newBalance, OutcomeApplied, nil
}

// ledgerKey is the Q-ABKEY dedup key: the event carries it pre-composed by the
// producer (he_request_id for a single request, {he_request_id}:{leg} for A/B).
// Falls back to he_request_id if the producer left it empty (defensive).
func ledgerKey(ev *billingv1.UsageEvent) string {
	if k := ev.GetLedgerKey(); k != "" {
		return k
	}
	return ev.GetHeRequestId()
}

// billingModeString maps the proto enum to the usage_ledger.billing_mode text.
func billingModeString(m billingv1.BillingMode) string {
	if m == billingv1.BillingMode_BILLING_MODE_PER_CALL {
		return "per_call"
	}
	return "per_token"
}
