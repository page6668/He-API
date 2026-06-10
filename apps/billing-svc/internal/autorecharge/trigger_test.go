// Story 7.7 AC1 — the auto-recharge trigger + storm fence tests. The dominant
// correctness risk is the at-most-one-in-flight invariant, proved here at both
// fence layers: the Redis SETNX fast gate (7.7-INT-020) and the DB partial-unique
// durable fence under Redis loss (7.7-INT-021). Plus the trigger decision
// (7.7-UNIT-020), decline→failed (7.7-INT-031), auto-disable (7.7-UNIT-040/041),
// suppression (7.7-INT-052), and the Q-ALERT-THRESHOLD env default (7.7-UNIT-051).
package autorecharge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/redis/go-redis/v9"
)

type fakeTokens struct {
	token, provider string
	found           bool
}

func (f fakeTokens) GetOwnedToken(_ context.Context, _, _ string) (string, string, bool, error) {
	return f.token, f.provider, f.found, nil
}

type fakeCharger struct {
	status string // "pending" | "failed"
	calls  int32
}

func (c *fakeCharger) ChargeOffSession(_ context.Context, _, _, _, _, _ string) (string, string, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.status == "failed" {
		return "", "failed", nil
	}
	return "pi_test", "pending", nil
}

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func configRows(enabled bool, thr, amt, pm string) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"enabled", "threshold", "amount", "pm"}).AddRow(enabled, thr, amt, pm)
}

// 7.7-UNIT-020 — trigger gating: below+enabled → trigger; above → no action;
// disabled+below → low-balance alert (no charge).
func TestEvaluate_TriggerDecision(t *testing.T) {
	ctx := context.Background()

	t.Run("below_and_enabled_triggers", func(t *testing.T) {
		mock, _ := pgxmock.NewPool()
		defer mock.Close()
		mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
		mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("o1"))
		tr := New(mock, newRedis(t), fakeTokens{"tok", "stripe", true}, &fakeCharger{status: "pending"}, "", nil)
		got := tr.Evaluate(ctx, "u1", "4.86")
		if got.Outcome != OutcomeTriggered || got.Alert != AlertNone {
			t.Fatalf("got %+v, want Triggered+AlertNone", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet: %v", err)
		}
	})

	t.Run("above_threshold_no_action", func(t *testing.T) {
		mock, _ := pgxmock.NewPool()
		defer mock.Close()
		mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
		tr := New(mock, newRedis(t), fakeTokens{"tok", "stripe", true}, &fakeCharger{status: "pending"}, "", nil)
		got := tr.Evaluate(ctx, "u1", "9.99")
		if got.Outcome != OutcomeNoAction || got.Alert != AlertNone {
			t.Fatalf("got %+v, want NoAction+AlertNone", got)
		}
	})

	t.Run("disabled_below_triggers_low_balance_alert", func(t *testing.T) {
		mock, _ := pgxmock.NewPool()
		defer mock.Close()
		mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(false, "5.00", "", ""))
		tr := New(mock, newRedis(t), fakeTokens{}, &fakeCharger{}, "", nil)
		got := tr.Evaluate(ctx, "u1", "4.50")
		if got.Outcome != OutcomeNoAction || got.Alert != AlertLowBalance {
			t.Fatalf("got %+v, want NoAction+AlertLowBalance", got)
		}
		if got.Threshold != "5.00" {
			t.Fatalf("alert threshold = %q, want 5.00", got.Threshold)
		}
	})
}

// 7.7-INT-020 — fast gate: of two observers crossing the threshold, the second
// finds the Redis lock held → FenceHeld → no second order, no second charge.
func TestEvaluate_StormFastGate(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb := newRedis(t)
	charger := &fakeCharger{status: "pending"}
	tr := New(mock, rdb, fakeTokens{"tok", "stripe", true}, charger, "", nil)

	// call 1 acquires the lock, inserts one order, charges once.
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
	mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("o1"))
	// call 2 reads config then finds the lock held — no insert expected.
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))

	if got := tr.Evaluate(ctx, "u1", "4.86"); got.Outcome != OutcomeTriggered {
		t.Fatalf("call1 = %+v, want Triggered", got)
	}
	if got := tr.Evaluate(ctx, "u1", "4.80"); got.Outcome != OutcomeFenceHeld {
		t.Fatalf("call2 = %+v, want FenceHeld", got)
	}
	if charger.calls != 1 {
		t.Fatalf("charger called %d times, want exactly 1 (at-most-one invariant)", charger.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.7-INT-021 — durable fence under Redis loss: with redis=nil (lock unavailable),
// a concurrent pending auto-order insert hits the partial-unique (23505) → the DB
// guard alone still prevents a double-start.
func TestEvaluate_DurableFenceRedisLoss(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
	mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").
		WillReturnError(&pgconn.PgError{Code: "23505"}) // unique_violation — pending auto already exists
	charger := &fakeCharger{status: "pending"}
	tr := New(mock, nil /* Redis lost */, fakeTokens{"tok", "stripe", true}, charger, "", nil)

	got := tr.Evaluate(ctx, "u1", "4.86")
	if got.Outcome != OutcomeFenceHeld {
		t.Fatalf("got %+v, want FenceHeld (durable DB fence)", got)
	}
	if charger.calls != 0 {
		t.Fatalf("charger called %d times, want 0 (no double-start on Redis loss)", charger.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.7-INT-020 (concurrency) — the SETNX gate is atomic: of N concurrent observers,
// exactly one acquires the lock.
func TestStormLock_ExactlyOneAcquires(t *testing.T) {
	rdb := newRedis(t)
	const N = 32
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := rdb.SetNX(context.Background(), lockKey("u1"), "1", time.Minute).Result()
			if err == nil && ok {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d goroutines acquired the lock, want exactly 1", wins)
	}
}

// 7.7-INT-031 — declined off-session charge → order marked failed, OutcomeFailed,
// the "auto-recharge failed" alert fires; not retried in a loop.
func TestEvaluate_ChargeDeclined(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
	mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("o1"))
	mock.ExpectExec("UPDATE he_api.recharge_orders SET status = 'failed'").WithArgs("o1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	tr := New(mock, newRedis(t), fakeTokens{"tok", "stripe", true}, &fakeCharger{status: "failed"}, "", nil)

	got := tr.Evaluate(ctx, "u1", "4.86")
	if got.Outcome != OutcomeFailed || got.Alert != AlertFailed {
		t.Fatalf("got %+v, want Failed+AlertFailed", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.7-UNIT-040 — auto-disable after N=3 consecutive failures.
func TestEvaluate_AutoDisableAfterN(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb := newRedis(t)
	tr := New(mock, rdb, fakeTokens{"tok", "stripe", true}, &fakeCharger{status: "failed"}, "", nil)

	for i := 1; i <= 3; i++ {
		mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
		mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("o1"))
		mock.ExpectExec("UPDATE he_api.recharge_orders SET status = 'failed'").WithArgs("o1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		if i == 3 {
			mock.ExpectExec("UPDATE he_api.balances SET auto_recharge_enabled = FALSE").WithArgs("u1").
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		}
		got := tr.Evaluate(ctx, "u1", "4.86")
		if i < 3 && got.Outcome != OutcomeFailed {
			t.Fatalf("attempt %d = %+v, want Failed", i, got)
		}
		if i == 3 && got.Outcome != OutcomeDisabled {
			t.Fatalf("attempt 3 = %+v, want Disabled", got)
		}
		// Re-acquire: the lock is released on failure so the next debit re-evaluates.
		rdb.Del(ctx, lockKey("u1"))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.7-UNIT-041 / 7.7-INT-052 — success resets the failure counter AND suppresses
// the low-balance alert (the 7.3 receipt is the user signal).
func TestEvaluate_SuccessSuppressesAlertAndResetsCounter(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb := newRedis(t)
	rdb.Set(ctx, failKey("u1"), "2", 0) // two prior failures

	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
	mock.ExpectQuery("INSERT INTO he_api.recharge_orders").WithArgs("u1", "20.0000", "stripe").WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("o1"))
	tr := New(mock, rdb, fakeTokens{"tok", "stripe", true}, &fakeCharger{status: "pending"}, "", nil)

	got := tr.Evaluate(ctx, "u1", "4.86")
	if got.Outcome != OutcomeTriggered || got.Alert != AlertNone {
		t.Fatalf("got %+v, want Triggered+AlertNone (suppression)", got)
	}
	if exists := rdb.Exists(ctx, failKey("u1")).Val(); exists != 0 {
		t.Fatalf("failure counter not reset on success")
	}
}

// 7.7-UNIT-051 / 7.7-BLIND-BOUNDARY-003 — Q-ALERT-THRESHOLD: auto-recharge OFF and
// threshold column NULL → fall back to the env system default; never a nil-deref.
func TestEvaluate_AlertThresholdEnvDefault(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(false, "", "", ""))
	tr := New(mock, newRedis(t), fakeTokens{}, &fakeCharger{}, "3.00" /* env default */, nil)

	got := tr.Evaluate(ctx, "u1", "2.50")
	if got.Outcome != OutcomeNoAction || got.Alert != AlertLowBalance {
		t.Fatalf("got %+v, want NoAction+AlertLowBalance via env default", got)
	}
	if got.Threshold != "3.00" {
		t.Fatalf("alert threshold = %q, want env default 3.00", got.Threshold)
	}
}

// 7.7-INT-002 (trigger side) — a configured-but-foreign/missing method token →
// no charge (cross-user binding can never resolve another user's token).
func TestEvaluate_TokenNotFound_NoCharge(t *testing.T) {
	ctx := context.Background()
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("auto_recharge_enabled").WithArgs("u1").WillReturnRows(configRows(true, "5.00", "20.00", "pm1"))
	charger := &fakeCharger{status: "pending"}
	tr := New(mock, newRedis(t), fakeTokens{found: false}, charger, "", nil)

	got := tr.Evaluate(ctx, "u1", "4.86")
	if got.Outcome != OutcomeNoAction {
		t.Fatalf("got %+v, want NoAction (token not resolvable)", got)
	}
	if charger.calls != 0 {
		t.Fatalf("charger called with no resolvable token")
	}
}
