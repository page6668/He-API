package fx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
	"github.com/shopspring/decimal"
)

func mustDec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return d
}

// stubProvider is a deterministic FxProvider for refresh tests.
type stubProvider struct {
	rate decimal.Decimal
	err  error
}

func (s stubProvider) Get(context.Context) (decimal.Decimal, error) { return s.rate, s.err }

// countingMetrics records outcomes so INT-031 can assert the right counter fired
// without an otel reader.
type countingMetrics struct {
	completed int
	failed    int
	reasons   []string
	lastAt    time.Time
}

func (c *countingMetrics) completedInc(context.Context) { c.completed++ }
func (c *countingMetrics) failedInc(_ context.Context, r string) {
	c.failed++
	c.reasons = append(c.reasons, r)
}
func (c *countingMetrics) recordSuccessAt(t time.Time) { c.lastAt = t }

// 7.2-UNIT-016 P0 — FX_MANUAL_USD_CNY bypasses the provider, returns the env rate.
func Test7_2_UNIT016_ManualOverride(t *testing.T) {
	p, err := ProviderFromEnv(func(k string) string {
		if k == "FX_MANUAL_USD_CNY" {
			return "7.25"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("ProviderFromEnv: %v", err)
	}
	rate, _ := p.Get(context.Background())
	if rate.String() != "7.25" {
		t.Fatalf("manual rate = %s, want 7.25", rate.String())
	}
}

// 7.2-UNIT-017 P0 — FX_MANUAL_USD_CNY unparseable / <=0 → fail-fast boot error.
func Test7_2_UNIT017_ManualFailFast(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-1", "  "} {
		if _, err := NewManualProvider(bad); err == nil {
			t.Errorf("NewManualProvider(%q) should fail-fast", bad)
		}
	}
	// ProviderFromEnv with no override and no URL → configuration error.
	if _, err := ProviderFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("ProviderFromEnv with nothing configured should error")
	}
}

// 7.2-INT-020 P0 — success → INSERT a new row; inserted=true; completed metric.
func Test7_2_INT020_RefreshSuccessInserts(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("INSERT INTO he_api.fx_rates").
		WithArgs("USD", "CNY", pgxmock.AnyArg(), "test-provider").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	m := &countingMetrics{}
	inserted, err := Refresh(context.Background(), stubProvider{rate: mustDec(t, "7.18")}, mock, "test-provider", m, nil, nil)
	if err != nil || !inserted {
		t.Fatalf("Refresh = (%v, %v), want (true, nil)", inserted, err)
	}
	if m.completed != 1 || m.failed != 0 || m.lastAt.IsZero() {
		t.Fatalf("metrics wrong: %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// 7.2-INT-021 P0 — provider TIMEOUT → NO insert, inserted=false, err=nil
// (stale-serve, cron exits 0); failed metric reason provider_error.
func Test7_2_INT021_StaleServeOnTimeout(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	// NO ExpectExec — a stale-serve must NOT write.

	m := &countingMetrics{}
	inserted, err := Refresh(context.Background(),
		stubProvider{err: context.DeadlineExceeded}, mock, "p", m, nil, nil)
	if inserted || err != nil {
		t.Fatalf("Refresh = (%v, %v), want (false, nil) [stale-serve]", inserted, err)
	}
	if m.failed != 1 || m.completed != 0 || (len(m.reasons) > 0 && m.reasons[0] != "provider_error") {
		t.Fatalf("metrics wrong: %+v", m)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("an INSERT happened on stale-serve: %v", err)
	}
}

// 7.2-INT-022 / INT-023 P0 — non-2xx + malformed → stale-serve (provider returns
// an error → no insert, exit 0).
func Test7_2_INT022_023_StaleServeOnProviderError(t *testing.T) {
	for _, e := range []error{errors.New("status 503"), ErrMalformed} {
		mock, _ := pgxmock.NewPool()
		m := &countingMetrics{}
		inserted, err := Refresh(context.Background(), stubProvider{err: e}, mock, "p", m, nil, nil)
		if inserted || err != nil || m.failed != 1 {
			t.Errorf("error %v: Refresh=(%v,%v) failed=%d, want stale-serve", e, inserted, err, m.failed)
		}
		mock.Close()
	}
}

// 7.2-INT-024 P0 — rate <= 0 → REJECT; NEVER persist or serve a zero/null rate
// (money-safety crux); failed reason non_positive_rate.
func Test7_2_INT024_RejectNonPositiveRate(t *testing.T) {
	for _, bad := range []string{"0", "-7.21"} {
		mock, _ := pgxmock.NewPool()
		m := &countingMetrics{}
		inserted, err := Refresh(context.Background(), stubProvider{rate: mustDec(t, bad)}, mock, "p", m, nil, nil)
		if inserted || err != nil {
			t.Errorf("rate %s: Refresh=(%v,%v), want (false,nil) no insert", bad, inserted, err)
		}
		if len(m.reasons) == 0 || m.reasons[0] != "non_positive_rate" {
			t.Errorf("rate %s: reason = %v, want non_positive_rate", bad, m.reasons)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("rate %s: an INSERT happened: %v", bad, err)
		}
		mock.Close()
	}
}

// 7.2-INT-025 P1 — same rate as last → still appends a (harmless) new row
// (append-only; reads take latest).
func Test7_2_INT025_SameRateAppends(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectExec("INSERT INTO he_api.fx_rates").
		WithArgs("USD", "CNY", pgxmock.AnyArg(), "p").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	inserted, err := Refresh(context.Background(), stubProvider{rate: mustDec(t, "7.20")}, mock, "p", &countingMetrics{}, nil, nil)
	if !inserted || err != nil {
		t.Fatalf("same-rate refresh must still append: (%v,%v)", inserted, err)
	}
}

// 7.2-INT-026 P0 — idempotency at day granularity: a same-day re-run appends
// ANOTHER row (no UPDATE-in-place; audit trail preserved). Two successful runs
// → two INSERTs.
func Test7_2_INT026_DayIdempotencyAppends(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	for i := 0; i < 2; i++ {
		mock.ExpectExec("INSERT INTO he_api.fx_rates").
			WithArgs("USD", "CNY", pgxmock.AnyArg(), "p").
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}
	for i := 0; i < 2; i++ {
		if _, err := Refresh(context.Background(), stubProvider{rate: mustDec(t, "7.19")}, mock, "p", &countingMetrics{}, nil, nil); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected two INSERTs (append-only): %v", err)
	}
}

// 7.2-INT-027 P1 — append-only: the refresh SQL is an INSERT, NEVER an UPDATE
// (no lost-update, audit trail retained).
func Test7_2_INT027_AppendOnlyNoUpdate(t *testing.T) {
	up := strings.ToUpper(insertSQL)
	if !strings.Contains(up, "INSERT INTO HE_API.FX_RATES") || strings.Contains(up, "UPDATE ") {
		t.Fatalf("refresh SQL must be append-only INSERT, got: %s", insertSQL)
	}
}

// 7.2-INT-031 P1 — observability: the completed counter fires on success and the
// failed counter (with reason) on stale-serve; last-success timestamp recorded.
func Test7_2_INT031_Observability(t *testing.T) {
	// success path
	mock, _ := pgxmock.NewPool()
	mock.ExpectExec("INSERT INTO he_api.fx_rates").WithArgs("USD", "CNY", pgxmock.AnyArg(), "p").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	ok := &countingMetrics{}
	_, _ = Refresh(context.Background(), stubProvider{rate: mustDec(t, "7.21")}, mock, "p", ok, nil, func() time.Time {
		return time.Date(2026, 6, 10, 0, 0, 5, 0, time.UTC)
	})
	mock.Close()
	if ok.completed != 1 || !ok.lastAt.Equal(time.Date(2026, 6, 10, 0, 0, 5, 0, time.UTC)) {
		t.Fatalf("success metrics wrong: %+v", ok)
	}
	// failure path
	bad := &countingMetrics{}
	m2, _ := pgxmock.NewPool()
	_, _ = Refresh(context.Background(), stubProvider{err: errors.New("boom")}, m2, "p", bad, nil, nil)
	m2.Close()
	if bad.failed != 1 || bad.completed != 0 {
		t.Fatalf("failure metrics wrong: %+v", bad)
	}

	// The real otel Metrics is nil-safe and registers without panicking.
	_ = NewMetrics()
	var nilM *Metrics
	nilM.completedInc(context.Background())
	nilM.failedInc(context.Background(), "x")
	nilM.recordSuccessAt(time.Now())
}
