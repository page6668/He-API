package analyticsquery

// Story 9.1b — AC1: GET /v1/me/usage/series handler tests.
//
// Implements the QA test-design skeleton (Turing, 2026-06-15). The
// highest-stakes scenario is the P0 IDOR fence (9.1b-INT-005, BR-CH-SERIES-3):
// the handler must query ONLY the JWT-resolved user's rows and must strict-reject
// a query-param user_id. Handler-level coverage uses a fakeSeriesStore; the
// SQL-correctness scenarios that genuinely need a live ClickHouse (rollup ties to
// raw, By Model rollup, status-class reads request_logs) are exercised in the
// `//go:build integration` sibling series_integration_test.go and are t.Skip'd
// here with a pointer (per the skeleton's "genuinely N/A → t.Skip" rule).
//
// Sibling helpers (authed, newReq) come from handler_test.go (same package).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// seriesCall records one store invocation so a test can assert the IDOR fence
// (userID), the forwarded grouping, the tz-resolved lower bound, and the tz.
type seriesCall struct {
	userID string
	gb     SeriesGroupBy
	since  time.Time
	loc    *time.Location
}

type fakeSeriesStore struct {
	calls  []seriesCall
	points []SeriesPoint
	err    error
}

func (f *fakeSeriesStore) Series(_ context.Context, userID string, gb SeriesGroupBy, since time.Time, loc *time.Location) ([]SeriesPoint, error) {
	f.calls = append(f.calls, seriesCall{userID, gb, since, loc})
	if f.err != nil {
		return nil, f.err
	}
	return f.points, nil
}

func ptr(s string) *string { return &s }

// ============================================================
// AC1: GET /v1/me/usage/series — P0 SECURITY (run first, fail fast)
// ============================================================

// 9.1b-INT-005 [P0] IDOR fence — HIGHEST STAKES (BR-CH-SERIES-3). The handler
// resolves user_id ONLY from the JWT sub: every group_by forwards the caller's
// id, and a ?user_id=<victim> param is strict-rejected before any store call.
func TestSeries_IDORFence_OnlyCallerRows(t *testing.T) {
	store := &fakeSeriesStore{points: []SeriesPoint{{Bucket: "2026-06-10", Requests: 3, TotalTokens: 9}}}
	h := NewSeriesHandler(store, nil, nil)

	// Honest requests across every grouping must each query with the JWT sub.
	for _, gb := range []string{"day", "model", "status"} {
		rr := httptest.NewRecorder()
		h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?group_by="+gb), "user-A"))
		if rr.Code != http.StatusOK {
			t.Fatalf("group_by=%s: code=%d body=%s", gb, rr.Code, rr.Body.String())
		}
	}
	if len(store.calls) != 3 {
		t.Fatalf("expected 3 store reads, got %d", len(store.calls))
	}
	for _, c := range store.calls {
		if c.userID != "user-A" {
			t.Fatalf("query not IDOR-fenced to user-A: %+v", c)
		}
	}

	// Attack — ?user_id=user-B (an injected victim id) must 400 and NEVER reach
	// the store (user_id is not an accepted query parameter — BR-CH-SERIES-3).
	store2 := &fakeSeriesStore{}
	h2 := NewSeriesHandler(store2, nil, nil)
	rr2 := httptest.NewRecorder()
	h2.HandleSeries(rr2, authed(newReq("/v1/me/usage/series?user_id=user-B"), "user-A"))
	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("query-param user_id must be 400, got %d", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), "400_invalid_request") {
		t.Fatalf("expected 400_invalid_request envelope, got %s", rr2.Body.String())
	}
	if len(store2.calls) != 0 {
		t.Fatal("store must NOT be queried when an unrecognized param is present")
	}
}

// Missing JWT → 401 (the store is never touched).
func TestSeries_NoJWT_401(t *testing.T) {
	store := &fakeSeriesStore{}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, newReq("/v1/me/usage/series")) // no WithUserID
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing JWT must be 401, got %d", rr.Code)
	}
	if len(store.calls) != 0 {
		t.Fatal("store queried without a JWT")
	}
}

// ============================================================
// AC1: core shape + rollups (P1)
// ============================================================

// 9.1b-INT-001 [P1] group_by=day → key=null buckets; full envelope
// {range, group_by, series:[{bucket,key,requests,total_tokens}]}; cost_usd is
// absent under the SM default (omitempty — BR-CH-SERIES-2 / H-1-R).
func TestSeries_GroupByDay_DailyBuckets(t *testing.T) {
	store := &fakeSeriesStore{points: []SeriesPoint{
		{Bucket: "2026-06-09", Key: nil, Requests: 5, TotalTokens: 50},
		{Bucket: "2026-06-10", Key: nil, Requests: 7, TotalTokens: 70},
	}}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?group_by=day"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if store.calls[0].gb != GroupByDay {
		t.Fatalf("store gb=%q, want day", store.calls[0].gb)
	}

	var resp seriesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Range != "30d" || resp.GroupBy != "day" || len(resp.Series) != 2 {
		t.Fatalf("envelope mismatch: %+v", resp)
	}
	if resp.Series[0].Key != nil {
		t.Fatalf("By Day key must be null, got %v", *resp.Series[0].Key)
	}
	if resp.Series[1].Requests != 7 || resp.Series[1].TotalTokens != 70 {
		t.Fatalf("point fields wrong: %+v", resp.Series[1])
	}
	// cost_usd is omitted entirely under the SM default (no cost series).
	if strings.Contains(rr.Body.String(), "cost_usd") {
		t.Fatalf("cost_usd must be absent under SM default: %s", rr.Body.String())
	}
	// "key":null is present (not omitted) so By Day points keep a stable shape.
	if !strings.Contains(rr.Body.String(), `"key":null`) {
		t.Fatalf("By Day point must carry key:null: %s", rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", rr.Header().Get("Cache-Control"))
	}
}

// 9.1b-INT-002 [P1] group_by=model → buckets keyed by model (one line per model).
func TestSeries_GroupByModel_KeyedByModel(t *testing.T) {
	store := &fakeSeriesStore{points: []SeriesPoint{
		{Bucket: "2026-06-10", Key: ptr("qwen-max"), Requests: 4, TotalTokens: 40},
		{Bucket: "2026-06-10", Key: ptr("deepseek-v3"), Requests: 2, TotalTokens: 20},
	}}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?group_by=model"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if store.calls[0].gb != GroupByModel {
		t.Fatalf("store gb=%q, want model", store.calls[0].gb)
	}
	var resp seriesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.GroupBy != "model" || len(resp.Series) != 2 {
		t.Fatalf("envelope: %+v", resp)
	}
	if resp.Series[0].Key == nil || *resp.Series[0].Key != "qwen-max" {
		t.Fatalf("By Model key wrong: %+v", resp.Series[0])
	}
}

// 9.1b-INT-003 [P1] group_by=status → keyed by status class (success vs error).
// The handler forwards gb=status; the request_logs-vs-agg SQL detail is proven
// against a live ClickHouse in series_integration_test.go.
func TestSeries_GroupByStatus_KeyedByStatusClass(t *testing.T) {
	store := &fakeSeriesStore{points: []SeriesPoint{
		{Bucket: "2026-06-10", Key: ptr(statusKeySuccess), Requests: 9, TotalTokens: 90},
		{Bucket: "2026-06-10", Key: ptr(statusKeyError), Requests: 1, TotalTokens: 0},
	}}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?group_by=status"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if store.calls[0].gb != GroupByStatus {
		t.Fatalf("store gb=%q, want status", store.calls[0].gb)
	}
	var resp seriesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	keys := map[string]bool{}
	for _, p := range resp.Series {
		if p.Key != nil {
			keys[*p.Key] = true
		}
	}
	if !keys[statusKeySuccess] || !keys[statusKeyError] {
		t.Fatalf("status keys missing success/error: %+v", resp.Series)
	}
}

// 9.1b-INT-004 [P1] day bucketing honours users.timezone (Q-TZ parity): the
// `since` lower bound passed to the store is the local-day start shifted back
// (range-1) days, expressed in UTC; the user's *Location is forwarded for the
// toDate() bucketing.
func TestSeries_DayBuckets_HonourUserTimezone(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai") // UTC+8, whole-hour offset
	store := &fakeSeriesStore{}
	tz := func(context.Context, string) *time.Location { return sh }
	// 2026-06-11T01:00:00Z == 2026-06-11 09:00 Shanghai → today start 06-10T16:00Z.
	fixed := time.Date(2026, 6, 11, 1, 0, 0, 0, time.UTC)
	h := NewSeriesHandler(store, tz, nil).WithClock(func() time.Time { return fixed })

	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?range=30d&group_by=day"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	// today (local) = 2026-06-11 00:00 +08; minus 29 days = 2026-05-13 00:00 +08
	// == 2026-05-12T16:00:00Z (the 30-day window spans today + 29 prior local days).
	wantSince := time.Date(2026, 5, 12, 16, 0, 0, 0, time.UTC)
	if got := store.calls[0].since; !got.Equal(wantSince) {
		t.Fatalf("tz lower bound: got %s want %s", got, wantSince)
	}
	if store.calls[0].loc == nil || store.calls[0].loc.String() != "Asia/Shanghai" {
		t.Fatalf("tz Location not forwarded: %v", store.calls[0].loc)
	}
}

// 9.1b-INT-007 [P1] DATA: hourly_agg day-rollup SUM == request_logs raw, per
// group dim. Genuinely requires a live ClickHouse (SQL aggregation correctness).
func TestSeries_Rollup_TiesToRawRows(t *testing.T) {
	t.Skip("9.1b-INT-007: SQL-rollup correctness needs a live ClickHouse — see series_integration_test.go (//go:build integration)")
}

// 9.1b-INT-006 [P1] ERROR: ClickHouse unavailable → 503_clickhouse_unavailable
// (reuse 9.1 envelope verbatim — rest-api-spec §5.1.2).
func TestSeries_ClickHouseUnavailable_503(t *testing.T) {
	store := &fakeSeriesStore{err: errors.New("dial tcp: connection refused")}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series"), "user-A"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "503_clickhouse_unavailable") {
		t.Fatalf("missing envelope code: %s", rr.Body.String())
	}
}

// ============================================================
// AC1: validation (P1/P2)
// ============================================================

// 9.1b-UNIT-001 [P1] defaults: absent group_by→day, absent range→30d.
func TestSeries_ParseDefaults(t *testing.T) {
	gb, days, rangeStr, bad, _ := parseSeriesQuery(url.Values{})
	if bad != "" {
		t.Fatalf("empty query must be valid, got badParam=%q", bad)
	}
	if gb != GroupByDay || days != 30 || rangeStr != "30d" {
		t.Fatalf("defaults wrong: gb=%q days=%d range=%q", gb, days, rangeStr)
	}
}

// 9.1b-UNIT-002 [P1] unknown group_by (e.g. "region") → 400_invalid_request.
func TestSeries_UnknownGroupBy_400(t *testing.T) {
	assertSeries400(t, "/v1/me/usage/series?group_by=region", "group_by")
}

// 9.1b-UNIT-003 [P1] range > 90d → 400_invalid_request (BR-RD-7 bound).
func TestSeries_RangeOverMax_400(t *testing.T) {
	assertSeries400(t, "/v1/me/usage/series?range=120d", "range")
}

// 9.1b-UNIT-004 [P2] malformed range ("abc", "30") → 400_invalid_request.
func TestSeries_MalformedRange_400(t *testing.T) {
	assertSeries400(t, "/v1/me/usage/series?range=abc", "range")
	assertSeries400(t, "/v1/me/usage/series?range=30", "range") // missing "d" suffix
}

// ============================================================
// AC1: blind spots [BLIND-SPOT]
// ============================================================

// 9.1b-BLIND-BOUNDARY-001 [P1] no history → series:[] empty array, HTTP 200.
func TestSeries_NoHistory_EmptySeries200(t *testing.T) {
	store := &fakeSeriesStore{points: nil} // no rows
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("no history must be 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"series":[]`) {
		t.Fatalf("empty history must render series:[] (never null): %s", rr.Body.String())
	}
}

// 9.1b-BLIND-BOUNDARY-002 [P1] range=91d (just beyond cap) → 400_invalid_request.
func TestSeries_Range91d_400(t *testing.T) {
	assertSeries400(t, "/v1/me/usage/series?range=91d", "range")
}

// 9.1b-BLIND-BOUNDARY-003 [P2] range=0d → 400; empty range value → documented
// default (30d, HTTP 200).
func TestSeries_RangeZeroOrEmpty(t *testing.T) {
	assertSeries400(t, "/v1/me/usage/series?range=0d", "range")

	store := &fakeSeriesStore{}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?range="), "user-A")) // present-but-empty
	if rr.Code != http.StatusOK {
		t.Fatalf("empty range value must fall back to the 30d default (200), got %d", rr.Code)
	}
	if store.calls[0].gb != GroupByDay {
		t.Fatalf("default grouping wrong: %q", store.calls[0].gb)
	}
}

// 9.1b-BLIND-ERROR-001 [P1] a CH query failure surfaces the 503 envelope. The
// pool-connection release (no leak) is a store-level guarantee (defer
// rows.Close() in series_store.go) proven end-to-end in series_integration_test.go.
func TestSeries_CHQueryFailure_PoolConnReleased(t *testing.T) {
	store := &fakeSeriesStore{err: errors.New("clickhouse: read: i/o timeout")}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq("/v1/me/usage/series?group_by=model"), "user-A"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("mid-query CH failure must be 503, got %d", rr.Code)
	}
}

// 9.1b-BLIND-DATA-001 [P1] By Model rollup ties to raw rows — needs live CH.
func TestSeries_ByModelRollup_TiesToRaw(t *testing.T) {
	t.Skip("9.1b-BLIND-DATA-001: By Model rollup correctness needs a live ClickHouse — see series_integration_test.go")
}

// 9.1b-BLIND-RESOURCE-001 [P2] the production read reuses the 9.1 ClickHouse pool
// (no second CH path opened — BR-CH-SERIES-1). Proven structurally: the same
// *chStore that OpenStore dialled for /summary + /logs also satisfies SeriesStore,
// so main.go reuses it via a type assertion rather than dialling again.
func TestSeries_ReusesNinePointOnePool_NoLeak(t *testing.T) {
	var _ SeriesStore = (*chStore)(nil) // compile-time: *chStore is a SeriesStore
	var s Store = (*chStore)(nil)       // the /summary store…
	if _, ok := s.(SeriesStore); !ok {  // …is reusable as the /series store
		t.Fatal("the 9.1 *chStore must also satisfy SeriesStore so main.go reuses one CH pool")
	}
}

// ============================================================
// AC1: CONDITIONAL — only if Architect rules Q-SERIES-COST = IN
// ============================================================

// 9.1b-INT-009 [P2][CONDITIONAL] per-bucket SUM(usage_ledger.cost_usd) via the
// existing gateway pgxpool, IDOR-fenced; NEVER request_logs.cost_usd (always 0).
// Under SM default (no cost series) this stays skipped.
func TestSeries_CostSeries_ReadsUsageLedger(t *testing.T) {
	t.Skip("CONDITIONAL on Q-SERIES-COST=IN; SM default = no cost series (9.1b-INT-009)")
}

// assertSeries400 drives a request expected to fail validation with
// 400_invalid_request and the given offending param name, with zero store calls.
func assertSeries400(t *testing.T, target, wantParam string) {
	t.Helper()
	store := &fakeSeriesStore{}
	h := NewSeriesHandler(store, nil, nil)
	rr := httptest.NewRecorder()
	h.HandleSeries(rr, authed(newReq(target), "user-A"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("%s: code=%d, want 400 (body=%s)", target, rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "400_invalid_request") {
		t.Fatalf("%s: missing 400_invalid_request envelope: %s", target, body)
	}
	if !strings.Contains(body, wantParam) {
		t.Fatalf("%s: error should name the %q param: %s", target, wantParam, body)
	}
	if len(store.calls) != 0 {
		t.Fatalf("%s: store must not be queried on a 400", target)
	}
}
