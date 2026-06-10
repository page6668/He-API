// Story 7.7 AC3 — invoice store tests. 7.7-UNIT-060 period boundary, 7.7-INT-060
// generation, 7.7-INT-061 exactly-once on re-run (ON CONFLICT → zero RETURNING),
// 7.7-INT-082 IDOR (foreign id → ErrNotFound → gateway 404).
package invoice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
)

// 7.7-UNIT-060 — cron fired 2026-07-01 00:00 UTC → period "2026-06" over the
// half-open [2026-06-01, 2026-07-01) UTC window.
func TestPeriodBounds(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	period, start, end := PeriodBounds(now)
	if period != "2026-06" {
		t.Fatalf("period = %q, want 2026-06", period)
	}
	if !start.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v", start)
	}
	if !end.Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end = %v", end)
	}
}

// 7.7-INT-060 — generation inserts one row per active user and RETURNS the new rows.
func TestGenerate_ReturnsNewInvoices(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	period, start, end := PeriodBounds(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	mock.ExpectQuery("INSERT INTO he_api.invoices").WithArgs(period, start, end).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "debit", "credit"}).
			AddRow("inv1", "u1", "4.5600", "20.0000"))

	s := New(mock)
	got, err := s.Generate(context.Background(), period, start, end)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "u1" || got[0].TotalDebitUSD != "4.5600" || got[0].TotalCreditUSD != "20.0000" {
		t.Fatalf("unexpected generated: %+v", got)
	}
}

// 7.7-INT-061 — a re-run inserts ZERO rows (ON CONFLICT) → nothing to render/email.
func TestGenerate_ExactlyOnceOnRerun(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	period, start, end := PeriodBounds(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	mock.ExpectQuery("INSERT INTO he_api.invoices").WithArgs(period, start, end).
		WillReturnRows(pgxmock.NewRows([]string{"id", "user_id", "debit", "credit"})) // ON CONFLICT → empty

	s := New(mock)
	got, err := s.Generate(context.Background(), period, start, end)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("re-run produced %d invoices, want 0 (exactly-once)", len(got))
	}
}

func TestList_ReturnsInvoices(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE user_id").WithArgs("u1").
		WillReturnRows(pgxmock.NewRows([]string{"id", "period", "debit", "credit", "currency", "status", "has_pdf", "created_at"}).
			AddRow("inv1", "2026-06", "4.5600", "20.0000", "USD", "emailed", true, "2026-07-01T00:00:00Z"))
	s := New(mock)
	list, err := s.List(context.Background(), "u1")
	if err != nil || len(list) != 1 || list[0].Period != "2026-06" || !list[0].HasPdf {
		t.Fatalf("List = %+v err=%v", list, err)
	}
}

// 7.7-INT-081 — owner fetch resolves the user's invoice.
func TestGetForOwner_Found(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE id").WithArgs("inv1", "u1").
		WillReturnRows(pgxmock.NewRows([]string{"period", "debit", "credit", "status", "key"}).
			AddRow("2026-06", "4.5600", "20.0000", "emailed", "invoices/u1/inv1.pdf"))
	s := New(mock)
	o, err := s.GetForOwner(context.Background(), "u1", "inv1")
	if err != nil || o.PdfObjectKey != "invoices/u1/inv1.pdf" {
		t.Fatalf("GetForOwner = %+v err=%v", o, err)
	}
}

// 7.7-INT-082 — IDOR: a foreign id → ErrNotFound (gateway → 404).
func TestGetForOwner_ForeignIsNotFound(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.invoices WHERE id").WithArgs("inv1", "attacker").
		WillReturnError(pgx.ErrNoRows)
	s := New(mock)
	_, err := s.GetForOwner(context.Background(), "attacker", "inv1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 7.7-INT-070 — generated→emailed advances once; a re-run affects zero rows.
func TestMarkEmailed_OnceGuard(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("UPDATE he_api.invoices").WithArgs("inv1", "invoices/u1/inv1.pdf").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0)) // already emailed → zero
	s := New(mock)
	advanced, err := s.MarkEmailed(context.Background(), "inv1", "invoices/u1/inv1.pdf")
	if err != nil || advanced {
		t.Fatalf("advanced=%v err=%v, want false/nil (no dup email)", advanced, err)
	}
}
