// Story 9.1 AC2 — usage summary handler tests. The highest-stakes is IDOR
// (9.1-INT-005): the handler must read ONLY the JWT-resolved user's rows and
// must never trust a query-param user_id.
package analyticsquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

type call struct {
	userID string
	since  time.Time
}

type fakeStore struct {
	todayCalls []call
	aggCalls   []call
	today      PeriodAgg
	agg        PeriodAgg
	err        error
}

func (f *fakeStore) Today(_ context.Context, userID string, since time.Time) (PeriodAgg, error) {
	f.todayCalls = append(f.todayCalls, call{userID, since})
	if f.err != nil {
		return PeriodAgg{}, f.err
	}
	return f.today, nil
}

func (f *fakeStore) AggSince(_ context.Context, userID string, since time.Time) (PeriodAgg, error) {
	f.aggCalls = append(f.aggCalls, call{userID, since})
	if f.err != nil {
		return PeriodAgg{}, f.err
	}
	return f.agg, nil
}

// fakeCostStore stands in for the PG usage_ledger SUM (H-1-R). It records every
// call (IDOR-on-PG + same-bounds assertions) and can be forced to error to
// exercise the R2-4 independent-degradation path.
type fakeCostStore struct {
	calls []call
	cost  decimal.Decimal
	err   error
}

func (f *fakeCostStore) CostSince(_ context.Context, userID string, since time.Time) (decimal.Decimal, error) {
	f.calls = append(f.calls, call{userID, since})
	if f.err != nil {
		return decimal.Zero, f.err
	}
	return f.cost, nil
}

// derefCost reads the nullable wire cost_usd for assertions.
func derefCost(t *testing.T, p period) string {
	t.Helper()
	if p.CostUsd == nil {
		t.Fatalf("cost_usd was null, wanted a value")
	}
	return *p.CostUsd
}

func authed(req *http.Request, userID string) *http.Request {
	return req.WithContext(middleware.WithUserID(req.Context(), userID))
}

func dec(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func newReq(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// 9.1-INT-005 — IDOR fence: BOTH stores (ClickHouse volume + PG usage_ledger
// 消费) are queried with the JWT sub ONLY (R2-6); a query-param user_id is
// strict-rejected (never reaches either store).
func TestIDORFence(t *testing.T) {
	store := &fakeStore{today: PeriodAgg{Requests: 5, Success: 5}}
	cost := &fakeCostStore{cost: dec("1.23")}
	h := NewHandler(store, cost, nil, nil)

	// Honest request — both stores must be called with the JWT sub on all 3 windows.
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(store.todayCalls) != 1 || store.todayCalls[0].userID != "user-A" {
		t.Fatalf("today not IDOR-fenced to user-A: %+v", store.todayCalls)
	}
	if len(store.aggCalls) != 2 || store.aggCalls[0].userID != "user-A" || store.aggCalls[1].userID != "user-A" {
		t.Fatalf("month/quarter not IDOR-fenced to user-A: %+v", store.aggCalls)
	}
	// R2-6 — the usage_ledger 消费 SUM is IDOR-fenced identically (3 windows).
	if len(cost.calls) != 3 {
		t.Fatalf("expected 3 usage_ledger cost reads, got %d", len(cost.calls))
	}
	for _, c := range cost.calls {
		if c.userID != "user-A" {
			t.Fatalf("cost read not IDOR-fenced to user-A: %+v", c)
		}
	}

	// Attack — ?user_id=user-B must be rejected and NEVER queried (either store).
	store2 := &fakeStore{}
	cost2 := &fakeCostStore{}
	h2 := NewHandler(store2, cost2, nil, nil)
	rr2 := httptest.NewRecorder()
	h2.HandleSummary(rr2, authed(newReq("/v1/me/usage/summary?user_id=user-B"), "user-A"))
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("query-param user_id must be 400, got %d", rr2.Code)
	}
	if len(store2.todayCalls)+len(store2.aggCalls)+len(cost2.calls) != 0 {
		t.Fatal("no store must be queried when a query param is present")
	}
}

// 9.1-INT-004 — summary reads today (request_logs direct) + month/quarter
// (hourly_agg) for volume, and usage_ledger for 消费, and returns the 3-window
// shape. ClickHouse no longer carries cost (PeriodAgg has none).
func TestSummaryThreeWindows(t *testing.T) {
	store := &fakeStore{
		today: PeriodAgg{Requests: 1234, Success: 1200, PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		agg:   PeriodAgg{Requests: 24890, Success: 24000, TotalTokens: 999},
	}
	cost := &fakeCostStore{cost: dec("0.42")}
	h := NewHandler(store, cost, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))

	var resp summaryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Today.Requests != 1234 || resp.Month.Requests != 24890 || resp.Quarter.Requests != 24890 {
		t.Fatalf("window mismatch: %+v", resp)
	}
	// 消费 comes from usage_ledger (H-1-R), formatted 4dp.
	if got := derefCost(t, resp.Today); got != "0.4200" {
		t.Fatalf("today cost = %q, want 0.4200", got)
	}
	// R2-1 — the usage_ledger SUM rode the SAME period bounds as the CH reads.
	if cost.calls[0].since != store.todayCalls[0].since {
		t.Fatalf("today cost bound %s != CH bound %s", cost.calls[0].since, store.todayCalls[0].since)
	}
	if cost.calls[1].since != store.aggCalls[0].since || cost.calls[2].since != store.aggCalls[1].since {
		t.Fatalf("month/quarter cost bounds diverge from CH bounds")
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", rr.Header().Get("Cache-Control"))
	}
}

// 9.1-UNIT-006 — zero-division guard: requests=0 → success_rate null (never NaN).
func TestZeroRequestsNullSuccessRate(t *testing.T) {
	store := &fakeStore{}                      // all-zero volume
	cost := &fakeCostStore{cost: decimal.Zero} // usage_ledger sums to 0
	h := NewHandler(store, cost, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))

	body := rr.Body.String()
	if !strings.Contains(body, `"success_rate":null`) {
		t.Fatalf("zero requests must render success_rate null: %s", body)
	}
	if strings.Contains(body, "NaN") || strings.Contains(body, "Inf") {
		t.Fatalf("must never emit NaN/Inf: %s", body)
	}
	// cost_usd reads the usage_ledger SUM (0 → "0.0000") — present, not null,
	// when usage_ledger is reachable (H-1-R).
	if !strings.Contains(body, `"cost_usd":"0.0000"`) {
		t.Fatalf("zero usage cost must be \"0.0000\": %s", body)
	}
}

// 9.1-UNIT-007 — wire shape: cost_usd + success_rate are JSON string-decimals,
// counts are integers, never JSON floats (Q-Spec-4).
func TestWireShapeStringDecimals(t *testing.T) {
	store := &fakeStore{today: PeriodAgg{Requests: 1234, Success: 1200}}
	cost := &fakeCostStore{cost: dec("0.42")}
	h := NewHandler(store, cost, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))
	body := rr.Body.String()
	if !strings.Contains(body, `"success_rate":"0.9724"`) {
		t.Fatalf("success_rate must be a string-decimal: %s", body)
	}
	if !strings.Contains(body, `"requests":1234`) {
		t.Fatalf("requests must be a JSON integer: %s", body)
	}
	// 9.1-INT-015 — client_ip (ops field) is NEVER in the read response.
	if strings.Contains(body, "client_ip") {
		t.Fatalf("response leaked client_ip: %s", body)
	}
}

// 9.1-INT-010 — ClickHouse unavailable → 503_clickhouse_unavailable.
func TestClickHouseDown503(t *testing.T) {
	store := &fakeStore{err: errors.New("dial tcp: connection refused")}
	h := NewHandler(store, &fakeCostStore{}, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "503_clickhouse_unavailable") {
		t.Fatalf("body missing envelope code: %s", rr.Body.String())
	}
}

// 9.1-INT-011 — strict-reject: any extra query param → 400_invalid_request.
func TestStrictRejectExtraParam(t *testing.T) {
	store := &fakeStore{}
	h := NewHandler(store, &fakeCostStore{}, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary?foo=bar"), "user-A"))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "400_invalid_request") {
		t.Fatalf("extra param must be 400_invalid_request, got %d %s", rr.Code, rr.Body.String())
	}
}

// 9.1-UNIT-012 — the 503 envelope is registered in openaierr.CodeMetadata (M-3
// runtime mirror; api_error class / 503).
func TestEnvelopeRegistered(t *testing.T) {
	meta, ok := openaierr.CodeMetadata["503_clickhouse_unavailable"]
	if !ok {
		t.Fatal("503_clickhouse_unavailable not registered in openaierr.CodeMetadata (M-3)")
	}
	if meta.HTTPStatus != 503 {
		t.Fatalf("HTTP status = %d, want 503", meta.HTTPStatus)
	}
}

// 9.1-INT-009 — period boundaries computed in the user's timezone (Q-TZ): the
// `since` passed to the store is the day/month/quarter start in that tz, in UTC.
func TestTimezoneBoundaries(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai") // UTC+8, whole-hour offset
	store := &fakeStore{}
	tz := func(context.Context, string) *time.Location { return sh }
	// 2026-06-11T01:00:00Z == 2026-06-11 09:00 Shanghai → today start 06-10T16:00Z.
	fixed := time.Date(2026, 6, 11, 1, 0, 0, 0, time.UTC)
	h := NewHandler(store, &fakeCostStore{}, tz, nil).WithClock(func() time.Time { return fixed })

	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	wantToday := time.Date(2026, 6, 10, 16, 0, 0, 0, time.UTC)
	if got := store.todayCalls[0].since; !got.Equal(wantToday) {
		t.Fatalf("today boundary in tz: got %s want %s", got, wantToday)
	}
	// Month start = 2026-06-01 00:00 +08:00 == 2026-05-31T16:00Z.
	wantMonth := time.Date(2026, 5, 31, 16, 0, 0, 0, time.UTC)
	if got := store.aggCalls[0].since; !got.Equal(wantMonth) {
		t.Fatalf("month boundary in tz: got %s want %s", got, wantMonth)
	}
	// Quarter (Apr-Jun) start = 2026-04-01 00:00 +08:00 == 2026-03-31T16:00Z.
	wantQuarter := time.Date(2026, 3, 31, 16, 0, 0, 0, time.UTC)
	if got := store.aggCalls[1].since; !got.Equal(wantQuarter) {
		t.Fatalf("quarter boundary in tz: got %s want %s", got, wantQuarter)
	}
}

// 9.1-T3.7 (R2-4) — 消费 degrades INDEPENDENTLY: a usage_ledger read error sets
// cost_usd=null for every period BUT the summary still returns 200 with the
// ClickHouse-sourced volume cells populated, and it does NOT emit
// 503_clickhouse_unavailable (that envelope is ClickHouse-specific).
func TestCostPartialDegradation(t *testing.T) {
	store := &fakeStore{today: PeriodAgg{Requests: 1234, Success: 1200, TotalTokens: 30}}
	cost := &fakeCostStore{err: errors.New("usage_ledger: connection reset")}
	h := NewHandler(store, cost, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))

	if rr.Code != http.StatusOK {
		t.Fatalf("usage_ledger error must NOT fail the summary; got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "503_clickhouse_unavailable") {
		t.Fatalf("usage_ledger fault must NOT emit the CH-specific 503 envelope: %s", body)
	}
	var resp summaryResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 消费 cells are null...
	if resp.Today.CostUsd != nil || resp.Month.CostUsd != nil || resp.Quarter.CostUsd != nil {
		t.Fatalf("cost_usd must be null on usage_ledger error: %s", body)
	}
	// ...while the ClickHouse-sourced volume cells stay live (independent degradation).
	if resp.Today.Requests != 1234 || resp.Today.Tokens.Total != 30 {
		t.Fatalf("CH volume cells must still render: %+v", resp.Today)
	}
	if resp.Today.SuccessRate == nil || *resp.Today.SuccessRate != "0.9724" {
		t.Fatalf("CH success_rate must still render: %+v", resp.Today.SuccessRate)
	}
}

// R2-4 — with NO cost store wired (e.g. the gateway PG pool is absent), 消费
// renders null rather than panicking; the CH cells still render.
func TestNilCostStoreRendersNull(t *testing.T) {
	store := &fakeStore{today: PeriodAgg{Requests: 5, Success: 5}}
	h := NewHandler(store, nil, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSummary(rr, authed(newReq("/v1/me/usage/summary"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"cost_usd":null`) {
		t.Fatalf("nil cost store must render cost_usd null: %s", rr.Body.String())
	}
}
