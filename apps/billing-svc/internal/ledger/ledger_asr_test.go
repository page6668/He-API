// Story 9.6 (T5.4) — the PER_MINUTE (ASR) event flows through the SAME
// exactly-once + atomic-debit ledger.Apply path as tokens (BR-3.6 — only the
// cost FORMULA differs). Covers 9.6-INT-012 (exactly-once + atomic) +
// 9.6-BLIND-DATA-001 (txn rollback) for the new billing dimension.
package ledger

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v3"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/billing-svc/internal/pricing"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

func asrSnapLedger(t *testing.T) *pricing.Snapshot {
	t.Helper()
	pm := decimal.RequireFromString("0.006000")
	return pricing.NewSnapshot(map[string]pricing.Row{
		"doubao-asr": {
			PriceIn:             decimal.Zero,
			PriceOut:            decimal.Zero,
			Markup:              decimal.RequireFromString("10.00"),
			PricePerMinuteAudio: &pm,
		},
	})
}

func asrEvent() *billingv1.UsageEvent {
	return &billingv1.UsageEvent{
		LedgerKey:            "req_ASR1",
		HeRequestId:          "req_ASR1",
		UserId:               "11111111-1111-1111-1111-111111111111",
		ApiKeyId:             "22222222-2222-2222-2222-222222222222",
		Model:                "doubao-asr",
		BillingMode:          billingv1.BillingMode_BILLING_MODE_PER_MINUTE,
		AudioDurationSeconds: 3.2, // ceil 4s → 4×0.006/60×1.1 = 0.0004
		Ts:                   "2026-06-15T12:00:00Z",
	}
}

// 9.6-INT-012 — ASR PER_MINUTE event: ONE ledger row + atomic balance debit by
// the duration-derived cost (0.0004), same Apply mechanics as tokens.
func TestApply_ASR_PerMinute_Happy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rdb, _ := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO he_api.balances").
		WithArgs("11111111-1111-1111-1111-111111111111", "0.0004"). // ASR cost flows through
		WillReturnRows(pgxmock.NewRows([]string{"current_usd"}).AddRow("9.9996"))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE he_api.api_keys").
		WithArgs("22222222-2222-2222-2222-222222222222", "0.0004").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	l := New(mock, rdb, stubPricer{asrSnapLedger(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), asrEvent())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != OutcomeApplied {
		t.Fatalf("outcome = %v, want Applied", res.Outcome)
	}
	if res.Cost.StringFixed(4) != "0.0004" {
		t.Fatalf("cost = %s, want 0.0004", res.Cost.StringFixed(4))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// 9.6-BLIND-DATA-001 / INT-012 — a redelivered ASR ledger_key inserts 0 rows →
// rollback, NO second debit (exactly-once for the PER_MINUTE path too).
func TestApply_ASR_ExactlyOnce(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO he_api.usage_ledger").
		WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 0)) // ON CONFLICT DO NOTHING
	mock.ExpectRollback()

	l := New(mock, rdb, stubPricer{asrSnapLedger(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), asrEvent())
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

// 9.6-UNIT-019 (through Apply) — an ASR event with no pricing → NoPricing
// outcome (DLQ; never a silent zero-cost ledger row).
func TestApply_ASR_NoPricing_DLQ(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rdb, _ := newRedis(t)
	// snapshot WITHOUT doubao-asr → ErrNoPricing → no PG touch.
	l := New(mock, rdb, stubPricer{qwenSnap(t)}, nil, NewMetrics())
	res, err := l.Apply(context.Background(), asrEvent())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != OutcomeNoPricing {
		t.Fatalf("outcome = %v, want NoPricing", res.Outcome)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
