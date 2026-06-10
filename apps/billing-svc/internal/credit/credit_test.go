// Story 7.3 credit-applier P0 lanes (the money-IN core, inverse of 7.1):
//   - 7.3-INT-004 happy: pending→paid flip + balance credit in one tx + mirror.
//   - 7.3-INT-013 exactly-once: a redelivery (zero-row flip) credits nothing.
//   - 7.3-INT-014 atomicity: a failure between flip + credit rolls BOTH back.
//   - 7.3-INT-017 amount-integrity (m2): settled ≠ intent → stay pending, NO
//     credit, NO paid flip, mismatch alert.
//   - 7.3-UNIT-003 CNY→USD conversion (HALF-UP, no float).
package credit

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/redis/go-redis/v9"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

const orderID = "55555555-5555-5555-5555-555555555555"
const userID = "11111111-1111-1111-1111-111111111111"

func rechargeEvent(settled, currency string) *paymentv1.PaymentEvent {
	return &paymentv1.PaymentEvent{
		OrderId:         orderID,
		PaymentProvider: "stripe",
		ExternalOrderId: "pi_123",
		SettledAmount:   settled,
		Currency:        currency,
		Status:          "paid",
		EventType:       "recharge_paid",
	}
}

// 7.3-INT-004 — happy: flip pending→paid + credit balance in ONE tx; mirror SET.
func TestApply_Recharge_Happy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rdb := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "50.0000", "USD"))
	mock.ExpectExec("UPDATE he_api.recharge_orders").
		WithArgs(orderID, "pi_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(userID, "50.0000").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("50.0000"))
	mock.ExpectCommit()

	a := New(mock, rdb, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("50.00", "USD"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeCredited {
		t.Fatalf("outcome = %v, want Credited", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
	if got, _ := rdb.Get(context.Background(), balanceRealtimeKey(userID)).Result(); got != "50.0000" {
		t.Fatalf("realtime mirror = %q, want 50.0000", got)
	}
}

// 7.3-INT-013 — EXACTLY-ONCE: a redelivery finds the order already paid (zero-row
// flip) → NO second credit; Apply returns Duplicate.
func TestApply_Recharge_ExactlyOnce_Redelivery(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "50.0000", "USD"))
	mock.ExpectExec("UPDATE he_api.recharge_orders").
		WithArgs(orderID, "pi_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0)) // already paid → zero rows
	// NO balance credit, NO commit (deferred rollback).
	mock.ExpectRollback()

	a := New(mock, nil, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("50.00", "USD"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeDuplicate {
		t.Fatalf("outcome = %v, want Duplicate", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.3-INT-014 — ATOMICITY: the balance credit fails → the whole tx rolls back
// (no half-flipped order). Apply surfaces the error so the consumer retains the
// offset and the provider/Kafka redelivers.
func TestApply_Recharge_Atomicity_CreditFails(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "50.0000", "USD"))
	mock.ExpectExec("UPDATE he_api.recharge_orders").
		WithArgs(orderID, "pi_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(userID, "50.0000").
		WillReturnError(errors.New("connection reset"))
	mock.ExpectRollback() // flip + credit both discarded

	a := New(mock, nil, nil)
	_, err := a.Apply(context.Background(), rechargeEvent("50.00", "USD"))
	if err == nil {
		t.Fatal("expected an error (PG fault), got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.3-INT-017 — AMOUNT INTEGRITY (m2): settled ≠ intent → NO paid flip, NO credit,
// order stays pending, mismatch alert. The tx reads the order then rolls back.
func TestApply_Recharge_AmountMismatch_Parks(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "50.0000", "USD"))
	// NO UPDATE (no flip), NO credit, NO commit — just the read then rollback.
	mock.ExpectRollback()

	a := New(mock, nil, nil)
	// Provider settled $0.01 against a $50 intent → tampering → park.
	out, err := a.Apply(context.Background(), rechargeEvent("0.01", "USD"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeMismatch {
		t.Fatalf("outcome = %v, want Mismatch (stay pending, no credit, no flip)", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// ROBUST-003 — a corrupt/unparseable stored order intent must NOT bypass the
// amount-integrity guard. The applier parks the order (no flip, no credit) and
// raises the mismatch alert, identical to a settled≠intent mismatch.
func TestApply_Recharge_UnparseableIntent_Parks(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "not-a-number", "USD"))
	// NO UPDATE (no flip), NO credit, NO commit — just the read then rollback.
	mock.ExpectRollback()

	a := New(mock, nil, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("50.00", "USD"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeMismatch {
		t.Fatalf("outcome = %v, want Mismatch (corrupt intent → stay pending, no credit, no flip)", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.5-INT-012 — CNY→USD fx-at-credit (the FIRST real exercise of the inherited
// toUSD branch, Story 7.5). A CNY settlement passes the amount-integrity guard in
// the PAID currency (settled 350 CNY == intent 350 CNY, Q-FX-INTENT), THEN converts
// to USD at the latest 7.2 fx_rate (350 / 7.00 = 50.0000, HALF-UP) and credits
// balances.current_usd. current_rmb is untouched (7.2 Q-SOT, single USD accounting).
func TestApply_Recharge_CNY_FxAtCredit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "350.0000", "CNY"))
	// Integrity passes in CNY → THEN the fx read converts the settled amount to USD.
	mock.ExpectQuery("SELECT rate::text FROM he_api.fx_rates").
		WithArgs("CNY").
		WillReturnRows(pgxmock.NewRows([]string{"rate"}).AddRow("7.00000000"))
	mock.ExpectExec("UPDATE he_api.recharge_orders").
		WithArgs(orderID, "pi_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(userID, "50.0000"). // 350 CNY / 7.00 = 50.0000 USD credited
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("50.0000"))
	mock.ExpectCommit()

	a := New(mock, rdb, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("350.00", "CNY"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeCredited {
		t.Fatalf("outcome = %v, want Credited (CNY→USD fx-at-credit)", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.5-INT-013 — Q-FX-INTENT: a SHORT CNY settlement parks, and the comparison
// happens in the PAID currency BEFORE any fx conversion (NO fx_rate query, NO flip)
// — so fx-rate drift can never falsely trip the park. settled 300 CNY ≠ intent 350
// CNY → mismatch.
func TestApply_Recharge_CNY_ShortSettle_ParksPreFx(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "350.0000", "CNY"))
	// NO fx_rate query (the park happens pre-conversion), NO UPDATE, NO credit, just rollback.
	mock.ExpectRollback()

	a := New(mock, nil, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("300.00", "CNY"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeMismatch {
		t.Fatalf("outcome = %v, want Mismatch (short CNY settle parks pre-fx)", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.6-INT-012 — HKD→USD fx-at-credit (the FIRST HKD exercise, Story 7.6; 港澳).
// REUSES the SAME inherited toUSD branch + paid-currency guard as CNY (credit.go
// UNCHANGED — the clean-reuse proof). A HKD settlement passes the integrity guard
// in the PAID currency (settled 390 HKD == intent 390 HKD), THEN converts at the
// latest 7.2 fx_rate (390 / 7.80 = 50.0000, HALF-UP) and credits current_usd.
// current_rmb is untouched (7.2 Q-SOT, single USD accounting).
func TestApply_Recharge_HKD_FxAtCredit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "390.0000", "HKD"))
	mock.ExpectQuery("SELECT rate::text FROM he_api.fx_rates").
		WithArgs("HKD").
		WillReturnRows(pgxmock.NewRows([]string{"rate"}).AddRow("7.80000000"))
	mock.ExpectExec("UPDATE he_api.recharge_orders").
		WithArgs(orderID, "pi_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs(userID, "50.0000"). // 390 HKD / 7.80 = 50.0000 USD credited
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("50.0000"))
	mock.ExpectCommit()

	a := New(mock, rdb, nil)
	out, err := a.Apply(context.Background(), rechargeEvent("390.00", "HKD"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeCredited {
		t.Fatalf("outcome = %v, want Credited (HKD→USD fx-at-credit)", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.6-INT-017 — Q-HKD-FX: an HKD settlement with NO HKD fx_rate row parks
// (fail-closed; no credit at an unknown rate). The fx read errors → Apply rolls
// back with no flip/credit. Inherited behaviour, HKD-first-exercised.
func TestApply_Recharge_HKD_MissingRate_Parks(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT user_id::text, amount::text, currency FROM he_api.recharge_orders").
		WithArgs(orderID).
		WillReturnRows(pgxmock.NewRows([]string{"user_id", "amount", "currency"}).AddRow(userID, "390.0000", "HKD"))
	// Integrity passes in HKD → the fx read finds NO HKD rate → error → fail-closed.
	mock.ExpectQuery("SELECT rate::text FROM he_api.fx_rates").
		WithArgs("HKD").
		WillReturnError(errors.New("no rows in result set"))
	mock.ExpectRollback()

	a := New(mock, nil, nil)
	// Fail-closed: a non-nil error rolls the tx back (no flip, no credit) and the
	// consumer retries — the order stays pending until an HKD rate exists.
	if _, err := a.Apply(context.Background(), rechargeEvent("390.00", "HKD")); err == nil {
		t.Fatal("missing HKD rate must fail-closed (no credit at unknown rate)")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 7.3-UNIT-003 — CNY→USD conversion at settlement (HALF-UP, decimal, no float).
// 360 CNY ÷ 7.20 = 50.00 USD. The conversion path is built + tested even though
// 7.3 enables USD only at the endpoint (Q-CURRENCY).
func TestToUSD_CNYConversion(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("SELECT rate::text FROM he_api.fx_rates").
		WithArgs("CNY").
		WillReturnRows(pgxmock.NewRows([]string{"rate"}).AddRow("7.20000000"))

	a := New(mock, nil, nil)
	usd, err := a.toUSD(context.Background(), "360.00", "CNY")
	if err != nil {
		t.Fatalf("toUSD: %v", err)
	}
	if usd.StringFixed(4) != "50.0000" {
		t.Fatalf("CNY 360 / 7.20 = %s USD, want 50.0000", usd.StringFixed(4))
	}
}

func TestToUSD_USDPassthrough(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	a := New(mock, nil, nil)
	usd, err := a.toUSD(context.Background(), "50.00", "USD")
	if err != nil {
		t.Fatalf("toUSD: %v", err)
	}
	if usd.StringFixed(4) != "50.0000" {
		t.Fatalf("USD passthrough = %s, want 50.0000", usd.StringFixed(4))
	}
}

// Subscription RAIL: an active event updates subscriptions status with NO balance
// credit (Q-SUBSCOPE). No tx, no balances touched.
func TestApply_SubscriptionActive_NoCredit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("UPDATE he_api.subscriptions").
		WithArgs("stripe", "sub_123").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	a := New(mock, nil, nil)
	ev := &paymentv1.PaymentEvent{
		EventType:              "subscription_active",
		PaymentProvider:        "stripe",
		ExternalSubscriptionId: "sub_123",
	}
	out, err := a.Apply(context.Background(), ev)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeSubscription {
		t.Fatalf("outcome = %v, want Subscription", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TEST-002 — subscription_past_due drives the subStatusSQL branch (a different
// code path from active) with status='past_due'. RAIL only: NO balance credit, no
// tx; idempotent re-assertion of a terminal status.
func TestApply_SubscriptionPastDue_NoCredit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("UPDATE he_api.subscriptions").
		WithArgs("stripe", "sub_123", "past_due").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	a := New(mock, nil, nil)
	ev := &paymentv1.PaymentEvent{
		EventType:              "subscription_past_due",
		PaymentProvider:        "stripe",
		ExternalSubscriptionId: "sub_123",
	}
	out, err := a.Apply(context.Background(), ev)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeSubscription {
		t.Fatalf("outcome = %v, want Subscription", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TEST-002 — subscription_cancel drives the subStatusSQL branch with
// status='cancelled'. RAIL only: NO balance credit, no tx.
func TestApply_SubscriptionCancel_NoCredit(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("UPDATE he_api.subscriptions").
		WithArgs("stripe", "sub_123", "cancelled").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	a := New(mock, nil, nil)
	ev := &paymentv1.PaymentEvent{
		EventType:              "subscription_cancel",
		PaymentProvider:        "stripe",
		ExternalSubscriptionId: "sub_123",
	}
	out, err := a.Apply(context.Background(), ev)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out != OutcomeSubscription {
		t.Fatalf("outcome = %v, want Subscription", out)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
