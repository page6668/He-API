package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v3"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func billingReq(t *testing.T, path string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	return req.WithContext(middleware.BearerWithUserID(req.Context(), "u-1"))
}

// fxStub is a deterministic FxRateSource for the Story-7.2 currency tests.
type fxStub struct {
	rate decimal.Decimal
	at   time.Time
	ok   bool
}

func (s fxStub) Lookup(base, quote string) (decimal.Decimal, time.Time, bool) {
	return s.rate, s.at, s.ok
}

func fxRMB(t *testing.T, rateStr string, at time.Time) Option {
	t.Helper()
	r, err := decimal.NewFromString(rateStr)
	if err != nil {
		t.Fatalf("bad rate %q: %v", rateStr, err)
	}
	return WithFxRateSource(fxStub{rate: r, at: at, ok: true})
}

var fxAsOf = time.Date(2026, 6, 9, 0, 0, 5, 0, time.UTC)

// ── Story 7.2 AC1 — currency-aware GET /v1/balance ────────────────────────────

// 7.2-INT-001 P0 — USD default (no selector) → 7.1 bytes PLUS additive
// {currency:USD, fx_rate:1.00000000, current_display==current_usd, fx_as_of}.
func Test7_2_INT001_BalanceUSDDefaultAdditive(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance"))

	var resp balanceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if rr.Code != 200 || resp.CurrentUSD != "12.3400" || resp.Currency != "USD" ||
		resp.FxRate != "1.00000000" || resp.CurrentDisplay != "12.3400" || resp.FxDegraded {
		t.Fatalf("USD default wrong: %+v", resp)
	}
}

// 7.2-INT-002 P0 — ?currency=rmb → converted display at the snapshot rate;
// fx_as_of == the rate's fetched_at.
func Test7_2_INT002_BalanceRMB(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=rmb"))

	var resp balanceResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || resp.Currency != "RMB" || resp.CurrentDisplay != "88.97" ||
		resp.FxRate != "7.21000000" || resp.FxAsOf != fxAsOf.Format(time.RFC3339) {
		t.Fatalf("RMB path wrong: %+v", resp)
	}
}

// 7.2-INT-003 P0 — current_usd (the authoritative truth) is ALWAYS present on
// the RMB path; *_display is additive, never a replacement (BR-B-3).
func Test7_2_INT003_CurrentUSDAlwaysPresent(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Now().UTC()))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=rmb"))

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rr.Body.Bytes(), &raw)
	if string(raw["current_usd"]) != `"12.3400"` {
		t.Fatalf("current_usd missing/wrong on RMB path: %s", raw["current_usd"])
	}
}

// 7.2-INT-004 P1 — ?currency=USD (explicit, upper-case) ≡ no-selector path.
func Test7_2_INT004_ExplicitUSDCaseInsensitive(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Now().UTC()))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=USD"))

	var resp balanceResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || resp.Currency != "USD" || resp.CurrentDisplay != "12.3400" {
		t.Fatalf("explicit USD wrong: %+v", resp)
	}
}

// 7.2-INT-005 P1 / BLIND-BOUNDARY-004 — ?currency=" RMB " whitespace-trimmed +
// case-insensitive → RMB; and ?currency= (empty) → USD default.
func Test7_2_INT005_WhitespaceAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		q        string
		wantCurr string
	}{
		{"/v1/balance?currency=%20RMB%20", "RMB"},
		{"/v1/balance?currency=", "USD"}, // empty distinguished from invalid (BLIND-BOUNDARY-004)
	} {
		mock, _ := pgxmock.NewPool()
		mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
			WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
				AddRow("1.0000", time.Now().UTC()))
		rr := httptest.NewRecorder()
		NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, tc.q))
		var resp balanceResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		if rr.Code != 200 || resp.Currency != tc.wantCurr {
			t.Errorf("%s → currency %q (code %d), want %q", tc.q, resp.Currency, rr.Code, tc.wantCurr)
		}
		mock.Close()
	}
}

// 7.2-INT-006 P1 — lazy-absent balances row + ?currency=rmb → current_usd
// "0.0000", current_display "0.00" (7.1 lazy-zero preserved).
func Test7_2_INT006_LazyAbsentRMB(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").WillReturnError(pgx.ErrNoRows)

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=rmb"))

	var resp balanceResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || resp.CurrentUSD != "0.0000" || resp.CurrentDisplay != "0.00" {
		t.Fatalf("lazy-absent RMB wrong: %+v", resp)
	}
}

// 7.2-INT-007 P0 — money discipline: current_usd, current_display, fx_rate ALL
// string-decimals (never JSON numbers).
func Test7_2_INT007_BalanceMoneyDiscipline(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Now().UTC()))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=rmb"))

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rr.Body.Bytes(), &raw)
	for _, f := range []string{"current_usd", "current_display", "fx_rate"} {
		if b := raw[f]; len(b) == 0 || b[0] != '"' {
			t.Errorf("%s must be a JSON string, got %s", f, b)
		}
	}
}

// ── Story 7.2 AC1 — currency-aware GET /v1/usage ──────────────────────────────

func expectUsageRows(mock pgxmock.PgxPoolIface, total string, byModel [][]any) {
	mock.ExpectQuery("FROM he_api.usage_ledger").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum", "count", "tokens"}).AddRow(total, 1, int64(100)))
	rows := pgxmock.NewRows([]string{"model", "sum", "count", "tokens"})
	for _, r := range byModel {
		rows.AddRow(r...)
	}
	mock.ExpectQuery("GROUP BY model").WithArgs("u-1").WillReturnRows(rows)
}

// 7.2-INT-008 P0 — GET /v1/usage USD default → 7.1 bytes + additive
// {currency:USD, fx_rate:1.00000000, total_cost_display==total_cost_usd}.
func Test7_2_INT008_UsageUSDDefault(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	expectUsageRows(mock, "4.5600", [][]any{{"qwen-max", "4.5600", 1, int64(100)}})

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Usage(rr, billingReq(t, "/v1/usage"))

	var resp usageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || resp.Currency != "USD" || resp.FxRate != "1.00000000" ||
		resp.TotalCostDisplay != "4.5600" || resp.TotalCostUSD != "4.5600" {
		t.Fatalf("usage USD default wrong: %+v", resp)
	}
}

// 7.2-INT-009 P0 — GET /v1/usage?currency=rmb → total_cost_display = total ×
// rate HALF-UP 2dp.
func Test7_2_INT009_UsageRMB(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	expectUsageRows(mock, "4.5600", [][]any{{"qwen-max", "4.5600", 1, int64(100)}})

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Usage(rr, billingReq(t, "/v1/usage?currency=rmb"))

	var resp usageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	// 4.56 × 7.21 = 32.8776 → HALF-UP 2dp = 32.88
	if rr.Code != 200 || resp.Currency != "RMB" || resp.TotalCostDisplay != "32.88" {
		t.Fatalf("usage RMB wrong: %+v", resp)
	}
}

// 7.2-INT-010 P1 [M-2] — mixed-currency contract: top-line total_cost_display is
// RMB BUT by_model[].cost_usd STAYS USD on ?currency=rmb (documented MVP cut).
func Test7_2_INT010_MixedCurrencyByModel(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	expectUsageRows(mock, "4.5600", [][]any{{"qwen-max", "4.5600", 1, int64(100)}})

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Usage(rr, billingReq(t, "/v1/usage?currency=rmb"))

	var resp usageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.ByModel) != 1 || resp.ByModel[0].CostUSD != "4.5600" {
		t.Fatalf("by_model must stay USD on RMB request: %+v", resp.ByModel)
	}
}

// 7.2-INT-011 P1 — GET /v1/usage zero rows + ?currency=rmb → total_cost_usd
// "0.0000", total_cost_display "0.00".
func Test7_2_INT011_UsageZeroRMB(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	expectUsageRows(mock, "0", nil)

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Usage(rr, billingReq(t, "/v1/usage?currency=rmb"))

	var resp usageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || resp.TotalCostUSD != "0.0000" || resp.TotalCostDisplay != "0.00" {
		t.Fatalf("usage zero RMB wrong: %+v", resp)
	}
}

// 7.2-INT-012 P0 — usage money discipline: every money + rate field a string.
func Test7_2_INT012_UsageMoneyDiscipline(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	expectUsageRows(mock, "4.5600", [][]any{{"qwen-max", "4.5600", 1, int64(100)}})

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Usage(rr, billingReq(t, "/v1/usage?currency=rmb"))

	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rr.Body.Bytes(), &raw)
	for _, f := range []string{"total_cost_usd", "total_cost_display", "fx_rate"} {
		if b := raw[f]; len(b) == 0 || b[0] != '"' {
			t.Errorf("%s must be a JSON string, got %s", f, b)
		}
	}
}

// ── Story 7.2 AC1 — unsupported-currency rejection (fail-loud) ─────────────────

// 7.2-INT-013 P0 — ?currency=eur → 400 400_unsupported_currency, rejected BEFORE
// any PG read (the mock has NO query expectation → a read would 500, not 400).
func Test7_2_INT013_BalanceUnsupportedBeforeRead(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, "/v1/balance?currency=eur"))

	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a PG read happened before currency validation: %v", err)
	}
}

// 7.2-INT-014 P0 — ?currency=xyz → 400; NO silent fallback to USD.
func Test7_2_INT014_BalanceArbitraryCurrency(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance?currency=xyz"))
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// 7.2-INT-015 P1 [M-1] — the 400 body matches the §5.1.2 envelope
// {type:invalid_request_error, code:400_unsupported_currency}.
func Test7_2_INT015_UnsupportedEnvelope(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance?currency=eur"))

	var env struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "400_unsupported_currency" || env.Error.Type != "invalid_request_error" {
		t.Fatalf("envelope wrong: %s", rr.Body.String())
	}
}

// 7.2-INT-016 P1 — GET /v1/usage?currency=eur → same 400 (both endpoints reject).
func Test7_2_INT016_UsageUnsupported(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Usage(rr, billingReq(t, "/v1/usage?currency=eur"))
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a PG read happened before currency validation: %v", err)
	}
}

// 7.2-BLIND-DATA-001 P0 (gateway side of INT-018) — SoT preservation: a display
// read (USD or RMB) issues SELECTs ONLY, NEVER a write. The read handler's DB
// seam (BillingReadQuerier) has no Exec method, so a story-7.2 path structurally
// CANNOT write balances.* / usage_ledger (BR-B-1 / Q-SOT). The full chat→deduct→
// reconcile byte-identical proof (7.2-INT-018) runs on testcontainers in CI.
func Test7_2_DATA001_DisplayReadIsWriteFree(t *testing.T) {
	for _, q := range []string{"/v1/balance", "/v1/balance?currency=rmb"} {
		mock, _ := pgxmock.NewPool()
		// ONLY a SELECT is expected; pgxmock flags any unexpected Exec.
		mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
			WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
				AddRow("12.3400", time.Now().UTC()))
		rr := httptest.NewRecorder()
		NewBillingReadHandler(nil, mock, fxRMB(t, "7.21000000", fxAsOf)).Balance(rr, billingReq(t, q))
		if rr.Code != 200 {
			t.Fatalf("%s: status %d", q, rr.Code)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%s: a write or extra query happened (SoT violation): %v", q, err)
		}
		mock.Close()
	}
}

// 7.2-INT-017 P1 — ?currency=rmb but NO fx_rates row → 200 degraded USD-only:
// current_display==current_usd, fx_rate:1.00000000, fx_degraded:true.
func Test7_2_INT017_ColdStartDegraded(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Now().UTC()))

	rr := httptest.NewRecorder()
	// fx source present but reports no rate (ok=false) → cold-start guard.
	h := NewBillingReadHandler(nil, mock, WithFxRateSource(fxStub{ok: false}))
	h.Balance(rr, billingReq(t, "/v1/balance?currency=rmb"))

	var resp balanceResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || !resp.FxDegraded || resp.FxRate != "1.00000000" ||
		resp.CurrentDisplay != resp.CurrentUSD {
		t.Fatalf("cold-start degraded wrong: %+v", resp)
	}
}

// 7.1-INT-021 / UNIT-019 — GET /v1/balance returns current_usd as a STRING
// decimal ("12.3400"), read from PG (authoritative). The money field must
// decode as a JSON string, never a number.
func TestBalance_StringMoneyFromPG(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"current_usd", "updated_at"}).
			AddRow("12.3400", time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	// Money MUST be a JSON string, not a number.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if string(raw["current_usd"]) != `"12.3400"` {
		t.Fatalf("current_usd = %s, want \"12.3400\" (string-decimal)", raw["current_usd"])
	}
}

// 7.1-INT-022 — absent balances row → 200 {"current_usd":"0.0000",...}.
func TestBalance_AbsentRowZero(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.balances").
		WithArgs("u-1").
		WillReturnError(pgx.ErrNoRows)

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Balance(rr, billingReq(t, "/v1/balance"))

	var resp struct {
		CurrentUSD string `json:"current_usd"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || resp.CurrentUSD != "0.0000" {
		t.Fatalf("absent row: status=%d current_usd=%q, want 200 / 0.0000", rr.Code, resp.CurrentUSD)
	}
}

// 7.1-INT-023 — GET /v1/usage aggregates totals + per-model breakdown.
func TestUsage_AggregateAndByModel(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.usage_ledger").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum", "count", "tokens"}).
			AddRow("4.5600", 132, int64(845000)))
	mock.ExpectQuery("GROUP BY model").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"model", "sum", "count", "tokens"}).
			AddRow("deepseek-v3", "1.5600", 50, int64(300000)).
			AddRow("qwen-max", "3.0000", 82, int64(545000)))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Usage(rr, billingReq(t, "/v1/usage"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Period        string `json:"period"`
		TotalCostUSD  string `json:"total_cost_usd"`
		TotalRequests int    `json:"total_requests"`
		TotalTokens   int64  `json:"total_tokens"`
		ByModel       []struct {
			Model   string `json:"model"`
			CostUSD string `json:"cost_usd"`
		} `json:"by_model"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.TotalCostUSD != "4.5600" || resp.TotalRequests != 132 || resp.TotalTokens != 845000 {
		t.Fatalf("totals wrong: %+v", resp)
	}
	if len(resp.ByModel) != 2 || resp.ByModel[0].CostUSD != "1.5600" {
		t.Fatalf("by_model wrong: %+v", resp.ByModel)
	}
}

// 7.1-INT-024 — GET /v1/usage with no rows → zero-aggregate.
func TestUsage_ZeroAggregate(t *testing.T) {
	mock, _ := pgxmock.NewPool()
	defer mock.Close()
	mock.ExpectQuery("FROM he_api.usage_ledger").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"sum", "count", "tokens"}).
			AddRow("0", 0, int64(0)))
	mock.ExpectQuery("GROUP BY model").
		WithArgs("u-1").
		WillReturnRows(pgxmock.NewRows([]string{"model", "sum", "count", "tokens"}))

	rr := httptest.NewRecorder()
	NewBillingReadHandler(nil, mock).Usage(rr, billingReq(t, "/v1/usage"))

	var resp struct {
		TotalCostUSD  string `json:"total_cost_usd"`
		TotalRequests int    `json:"total_requests"`
		ByModel       []any  `json:"by_model"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != http.StatusOK || resp.TotalCostUSD != "0.0000" || resp.TotalRequests != 0 || len(resp.ByModel) != 0 {
		t.Fatalf("zero aggregate wrong: status=%d %+v", rr.Code, resp)
	}
}
