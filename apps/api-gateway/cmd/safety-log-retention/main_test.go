package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v3"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// 8.5-UNIT-010 — the sweep issues the strict 6-month range DELETE and exits 0,
// reporting the rows deleted. (The actual 7mo-deleted / 5mo-kept row semantics
// live in the WHERE clause asserted by 8.5-BLIND-BOUNDARY-004; against a real DB
// they are a documented Dev follow-up per the toolchain caveat.)
func Test8_5_UNIT010_SweepDeletesAndExitsZero(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("DELETE FROM he_api.content_safety_logs").
		WillReturnResult(pgxmock.NewResult("DELETE", 7))

	if code := sweep(context.Background(), testLogger(), mock); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 8.5-UNIT-011 / 8.5-BLIND-BOUNDARY-005 — idempotent / empty: re-run within the
// window (or an empty table) deletes 0 rows and still exits 0.
func Test8_5_UNIT011_SweepIdempotentEmptyExitsZero(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("DELETE FROM he_api.content_safety_logs").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	if code := sweep(context.Background(), testLogger(), mock); code != 0 {
		t.Fatalf("exit code = %d, want 0 on empty sweep", code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// 8.5-UNIT-012 / 8.5-BLIND-ERROR-003 — DB unreachable in cron → exit 1, no
// partial effect (CronJob backoffLimit:0 → human-verified).
func Test8_5_UNIT012_SweepDBErrorExitsOne(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("DELETE FROM he_api.content_safety_logs").
		WillReturnError(errors.New("connection refused"))

	if code := sweep(context.Background(), testLogger(), mock); code != 1 {
		t.Fatalf("exit code = %d, want 1 on DB failure", code)
	}
}

// 8.5-UNIT-012 (config) — HE_API_DB_POSTGRES_URI unset → exit 1 before any DB work.
func Test8_5_UNIT012_RunConfigErrorExitsOne(t *testing.T) {
	t.Setenv("HE_API_DB_POSTGRES_URI", "")
	if code := run(context.Background(), testLogger()); code != 1 {
		t.Fatalf("exit code = %d, want 1 on missing DSN", code)
	}
}

// 8.5-BLIND-BOUNDARY-004 — the cutoff is a STRICT half-open `<` bound at exactly
// 6 months: a row at the boundary is retained. Asserted statically on the SQL so
// a future edit to `<=` (which would over-delete by one tick) fails here.
func Test8_5_BLIND_BOUNDARY004_StrictSixMonthCutoff(t *testing.T) {
	if !strings.Contains(retentionSQL, "< NOW() - INTERVAL '6 months'") {
		t.Fatalf("retention SQL lost the strict 6-month cutoff: %q", retentionSQL)
	}
	if strings.Contains(retentionSQL, "<=") {
		t.Fatalf("retention cutoff must be strict `<`, not `<=`: %q", retentionSQL)
	}
}
