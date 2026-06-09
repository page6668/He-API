package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/billing-svc/internal/pricing"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

type stubPricer struct{ snap *pricing.Snapshot }

func (s stubPricer) Current() *pricing.Snapshot { return s.snap }

// anyN returns n AnyArg matchers — used where the test asserts control flow
// (commit/rollback/outcome), not exact bind values.
func anyN(n int) []any {
	a := make([]any, n)
	for i := range a {
		a[i] = pgxmock.AnyArg()
	}
	return a
}

// qwenSnap is the AC1 golden snapshot: qwen-max → cost 0.0343 for (1500,800).
func qwenSnap(t *testing.T) *pricing.Snapshot {
	t.Helper()
	mk := func(s string) decimal.Decimal {
		d, err := decimal.NewFromString(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	return pricing.NewSnapshot(map[string]pricing.Row{
		"qwen-max": {PriceIn: mk("0.0080"), PriceOut: mk("0.0240"), Markup: mk("10.00")},
	})
}

func qwenEvent() *billingv1.UsageEvent {
	return &billingv1.UsageEvent{
		LedgerKey:        "req_H1",
		HeRequestId:      "req_H1",
		UserId:           "11111111-1111-1111-1111-111111111111",
		ApiKeyId:         "22222222-2222-2222-2222-222222222222",
		Model:            "qwen-max",
		PromptTokens:     1500,
		CompletionTokens: 800,
		TotalTokens:      2300,
		Ts:               "2026-06-09T12:00:00Z",
		BillingMode:      billingv1.BillingMode_BILLING_MODE_PER_TOKEN,
	}
}

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()}), mr
}

// expectHappyTx wires a full successful deduction tx (insert 1 row → balance
// returns newBalance → commit → api_keys increment).
func expectHappyTx(mock pgxmock.PgxPoolIface, newBalance string) {
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(anyN(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow(newBalance))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE he_api.api_keys").
		WithArgs(anyN(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

// 7.1-INT-001 — happy path: one ledger row, balance debited by the golden cost,
// realtime mirror reconciled from the post-deduction PG value (Q-RECON).
func TestApply_Happy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rdb, _ := newRedis(t)

	// Assert the cost ("0.0343") is the exact bind value passed to the balance
	// deduction (the golden flows through, not just any number).
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs("11111111-1111-1111-1111-111111111111", "0.0343").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("9.9657"))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE he_api.api_keys").
		WithArgs("22222222-2222-2222-2222-222222222222", "0.0343").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), qwenEvent())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != OutcomeApplied {
		t.Fatalf("outcome = %v, want Applied", res.Outcome)
	}
	if res.Cost.StringFixed(4) != "0.0343" {
		t.Fatalf("cost = %s, want 0.0343", res.Cost.StringFixed(4))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
	got, _ := rdb.Get(context.Background(), BalanceRealtimeKey(qwenEvent().UserId)).Result()
	if got != "9.9657" {
		t.Fatalf("realtime mirror = %q, want 9.9657", got)
	}
}

// 7.1-INT-002 — EXACTLY-ONCE: a redelivered ledger_key inserts zero rows → no
// second deduction; the tx rolls back and Apply returns Duplicate (BR-D-1).
func TestApply_ExactlyOnce_Redelivery(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 0)) // ON CONFLICT → 0 rows
	mock.ExpectRollback()
	// NO balance UPDATE, NO commit, NO api_keys increment.

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), qwenEvent())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != OutcomeDuplicate {
		t.Fatalf("outcome = %v, want Duplicate", res.Outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.1-INT-004 / BLIND-DATA-001 — ATOMICITY: a failure between the ledger-insert
// and the balance-update rolls back BOTH (no orphan row, no phantom charge). The
// tx never commits and Apply surfaces the error (consumer will NOT ACK).
func TestApply_Atomicity_RollbackOnBalanceError(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(anyN(2)...).
		WillReturnError(errors.New("deadlock"))
	mock.ExpectRollback()
	// NO commit, NO api_keys increment (post-commit fan-out never reached).

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	_, err := l.Apply(context.Background(), qwenEvent())
	if err == nil {
		t.Fatal("expected error so the consumer does NOT ACK")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.1-INT-005 — PG down at Begin → error → consumer does not ACK (redelivery).
func TestApply_PGDownAtBegin(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	mock.ExpectBegin().WillReturnError(errors.New("pg unavailable"))

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	if _, err := l.Apply(context.Background(), qwenEvent()); err == nil {
		t.Fatal("expected error when PG is down at begin")
	}
}

// 7.1-INT-006 / BLIND-DATA-002 — Redis down POST-commit: the PG charge STANDS
// (Apply returns Applied, no error); the mirror failure is swallowed. The
// api_keys best-effort PG increment still runs.
func TestApply_RedisDownPostCommit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, mr := newRedis(t)

	expectHappyTx(mock, "9.9657")
	mr.Close() // Redis is now unreachable for the post-commit fan-out.

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), qwenEvent())
	if err != nil {
		t.Fatalf("Apply must succeed despite Redis-down post-commit: %v", err)
	}
	if res.Outcome != OutcomeApplied {
		t.Fatalf("outcome = %v, want Applied (durable PG charge stands)", res.Outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.1-INT-007 / BLIND-FLOW-002 — lazy balances UPSERT: an absent row is created
// at -cost on first deduction (no backfill migration).
func TestApply_LazyBalanceCreate(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	expectHappyTx(mock, "-0.0343") // lazy row created at -cost

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), qwenEvent())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != OutcomeApplied {
		t.Fatalf("outcome = %v, want Applied", res.Outcome)
	}
	got, _ := rdb.Get(context.Background(), BalanceRealtimeKey(qwenEvent().UserId)).Result()
	if got != "-0.0343" {
		t.Fatalf("realtime mirror = %q, want -0.0343 (lazy-created negative)", got)
	}
}

// 7.1-INT-009 — ErrNoPricing → OutcomeNoPricing, NO PG tx at all (never a silent
// zero-charge; consumer dead-letters).
func TestApply_NoPricing(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	ev := qwenEvent()
	ev.Model = "unknown-model"
	res, err := l.Apply(context.Background(), ev)
	if err != nil {
		t.Fatalf("Apply must not error on NoPricing (it DLQs): %v", err)
	}
	if res.Outcome != OutcomeNoPricing {
		t.Fatalf("outcome = %v, want NoPricing", res.Outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PG work on NoPricing: %v", err)
	}
}

// 7.1-INT-008 — post-commit fan-out increments the Story-5.2 month-cost counter.
func TestApply_MonthCostCounterIncrement(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	expectHappyTx(mock, "9.9657")

	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	if _, err := l.Apply(context.Background(), qwenEvent()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := rdb.Get(context.Background(), MonthCostKey(qwenEvent().ApiKeyId)).Result()
	if got != "0.0343" {
		t.Fatalf("month-cost counter = %q, want 0.0343", got)
	}
}
