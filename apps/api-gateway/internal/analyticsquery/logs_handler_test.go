// Story 9.2 AC1 — GET /v1/me/usage/logs handler tests. The headline risk is
// IDOR (BR-RD-1): the handler must read ONLY the JWT-resolved user's rows and
// must never trust a query-param user_id. PII exclusion (BR-RD-9) and the
// deep-pagination bound (BR-RD-2) are the other SM-elevated P0s.
package analyticsquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// fakeLogsStore records every call (IDOR assertions) and returns canned rows.
type logsCall struct {
	userID string
	filter LogsFilter
}

type fakeLogsStore struct {
	calls []logsCall
	items []LogEntry
	total int
	err   error
}

func (f *fakeLogsStore) Logs(_ context.Context, userID string, filter LogsFilter) ([]LogEntry, int, error) {
	f.calls = append(f.calls, logsCall{userID, filter})
	if f.err != nil {
		return nil, 0, f.err
	}
	return f.items, f.total, nil
}

func authedLogs(req *http.Request, userID string) *http.Request {
	return req.WithContext(middleware.WithUserID(req.Context(), userID))
}

func logsReq(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func decodeLogs(t *testing.T, body []byte) logsResponse {
	t.Helper()
	var r logsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, string(body))
	}
	return r
}

// 9.2-UNIT-001 — IDOR fence: a query-param user_id (or ANY unrecognized param)
// is strict-rejected with 400_invalid_request and NEVER reaches the store;
// user_id is resolved only from the JWT sub.
func TestLogsIDORRejectsUserIDParam(t *testing.T) {
	for _, target := range []string{
		"/v1/me/usage/logs?user_id=user-B",
		"/v1/me/usage/logs?foo=bar",
		"/v1/me/usage/logs?limit=10&user_id=user-B",
	} {
		store := &fakeLogsStore{}
		h := NewLogsHandler(store, nil)
		rr := httptest.NewRecorder()
		h.HandleLogs(rr, authedLogs(logsReq(target), "user-A"))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, want 400", target, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "400_invalid_request") {
			t.Fatalf("%s: body missing 400_invalid_request: %s", target, rr.Body.String())
		}
		if len(store.calls) != 0 {
			t.Fatalf("%s: store must NOT be queried when a bad param is present", target)
		}
	}
}

// 9.2-UNIT-006 — missing/expired JWT → 401_unauthenticated (no store call).
func TestLogsUnauthenticated(t *testing.T) {
	store := &fakeLogsStore{}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, logsReq("/v1/me/usage/logs")) // no user in context
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "401_unauthenticated") {
		t.Fatalf("body missing 401_unauthenticated: %s", rr.Body.String())
	}
	if len(store.calls) != 0 {
		t.Fatal("store must not be queried for an unauthenticated request")
	}
}

// 9.2-UNIT-002 — the store is always called with the JWT sub (the fence is
// applied unconditionally by the query builder; see TestLogsWhereAlwaysFenced).
func TestLogsFencedToJWTSub(t *testing.T) {
	store := &fakeLogsStore{items: []LogEntry{{HeRequestID: "req_a"}}, total: 1}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, authedLogs(logsReq("/v1/me/usage/logs?model=qwen-max"), "user-A"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(store.calls) != 1 || store.calls[0].userID != "user-A" {
		t.Fatalf("store not IDOR-fenced to user-A: %+v", store.calls)
	}
}

// 9.2-UNIT-003 — PII exclusion: a 200 response carries ONLY the 13 BR-RD-9
// fields; client_ip / client_country / user_agent / error_message / cost_usd are
// NEVER present in the serialized JSON.
func TestLogsResponseExcludesPII(t *testing.T) {
	store := &fakeLogsStore{
		items: []LogEntry{{
			HeRequestID: "req_aaaaaaaaaaaa", Ts: time.Now().UTC(), Model: "qwen-max",
			UpstreamModel: "qwen-max", StatusCode: 200, IsStreaming: true,
			PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30,
			LatencyMsTotal: 120, TtfbMs: 40, ApiKeyID: "22222222-2222-2222-2222-222222222222",
			ErrorCode: "",
		}},
		total: 1,
	}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, authedLogs(logsReq("/v1/me/usage/logs"), "user-A"))
	body := rr.Body.String()
	for _, banned := range []string{"client_ip", "client_country", "user_agent", "error_message", "cost_usd"} {
		if strings.Contains(body, banned) {
			t.Fatalf("response leaked PII/ops/cost field %q: %s", banned, body)
		}
	}

	// The LogEntry JSON object must expose EXACTLY the 13 BR-RD-9 keys.
	resp := decodeLogs(t, rr.Body.Bytes())
	raw, _ := json.Marshal(resp.Items[0])
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("re-decode item: %v", err)
	}
	want := []string{
		"he_request_id", "ts", "model", "upstream_model", "status_code", "is_streaming",
		"prompt_tokens", "completion_tokens", "total_tokens", "latency_ms_total",
		"ttfb_ms", "api_key_id", "error_code",
	}
	if len(keyed) != len(want) {
		t.Fatalf("LogEntry has %d fields, want %d: %v", len(keyed), len(want), keyed)
	}
	for _, k := range want {
		if _, ok := keyed[k]; !ok {
			t.Fatalf("LogEntry missing required field %q", k)
		}
	}
}

// 9.2-UNIT-004 — pagination cap (BOUNDARY-004): offset+limit ≤ 1000 enforced;
// offset=1000 → 400; offset=950&limit=100 → 400. param=offset in both.
func TestLogsPaginationCap(t *testing.T) {
	cases := []string{
		"/v1/me/usage/logs?offset=1000",
		"/v1/me/usage/logs?offset=950&limit=100",
		"/v1/me/usage/logs?offset=999&limit=2", // 1001 > 1000
	}
	for _, target := range cases {
		store := &fakeLogsStore{}
		h := NewLogsHandler(store, nil)
		rr := httptest.NewRecorder()
		h.HandleLogs(rr, authedLogs(logsReq(target), "user-A"))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, want 400", target, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `"param":"offset"`) {
			t.Fatalf("%s: expected param=offset, got %s", target, rr.Body.String())
		}
		if len(store.calls) != 0 {
			t.Fatalf("%s: store must not be queried on a bad offset", target)
		}
	}
}

// 9.2-UNIT-005 — window math: total_count is the store-capped value;
// has_more = offset + len(items) < total_count.
func TestLogsWindowMath(t *testing.T) {
	// Store reports the cap (1000) for a 2000-match set; first page of 50.
	page := make([]LogEntry, 50)
	store := &fakeLogsStore{items: page, total: logsWindowCap}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, authedLogs(logsReq("/v1/me/usage/logs?limit=50&offset=0"), "user-A"))
	resp := decodeLogs(t, rr.Body.Bytes())
	if resp.TotalCount != 1000 {
		t.Fatalf("total_count=%d, want 1000 (capped)", resp.TotalCount)
	}
	if !resp.HasMore {
		t.Fatalf("has_more must be true: 0+50 < 1000")
	}

	// Last reachable page: offset=950, 50 rows → 950+50=1000, not < 1000 → false.
	store2 := &fakeLogsStore{items: make([]LogEntry, 50), total: logsWindowCap}
	h2 := NewLogsHandler(store2, nil)
	rr2 := httptest.NewRecorder()
	h2.HandleLogs(rr2, authedLogs(logsReq("/v1/me/usage/logs?limit=50&offset=950"), "user-A"))
	resp2 := decodeLogs(t, rr2.Body.Bytes())
	if resp2.HasMore {
		t.Fatalf("has_more must be false at the window edge: %+v", resp2)
	}
}

// 9.2-UNIT-019-server (empty) — total_count=0 → empty items array (never null),
// has_more false.
func TestLogsEmptyResult(t *testing.T) {
	store := &fakeLogsStore{items: nil, total: 0}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, authedLogs(logsReq("/v1/me/usage/logs?api_key_id=22222222-2222-2222-2222-222222222222"), "user-B"))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"items":[]`) {
		t.Fatalf("empty result must serialize items as [], got %s", rr.Body.String())
	}
	resp := decodeLogs(t, rr.Body.Bytes())
	if resp.TotalCount != 0 || resp.HasMore {
		t.Fatalf("empty: total_count=%d has_more=%v", resp.TotalCount, resp.HasMore)
	}
}

// 9.2-INT-006 (handler half) — a store error → 503_clickhouse_unavailable.
func TestLogsClickHouseDown503(t *testing.T) {
	store := &fakeLogsStore{err: errors.New("dial tcp: connection refused")}
	h := NewLogsHandler(store, nil)
	rr := httptest.NewRecorder()
	h.HandleLogs(rr, authedLogs(logsReq("/v1/me/usage/logs"), "user-A"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "503_clickhouse_unavailable") {
		t.Fatalf("body missing envelope code: %s", rr.Body.String())
	}
}

// 9.2-UNIT-007..014 — param validation table: each invalid field → 400 with the
// correct `param`. Valid combos parse with the expected defaults/values.
func TestLogsParamValidation(t *testing.T) {
	bad := []struct {
		query string
		param string
	}{
		{"limit=0", "limit"},
		{"limit=101", "limit"},
		{"limit=abc", "limit"},
		{"offset=-1", "offset"},
		{"offset=abc", "offset"},
		{"status=teapot", "status"},
		{"is_streaming=maybe", "is_streaming"},
		{"api_key_id=not-a-uuid", "api_key_id"},
		{"start=not-a-date", "start"},
		{"end=not-a-date", "end"},
		{"start=2026-06-11T00:00:00Z&end=2026-06-10T00:00:00Z", "start"}, // end ≤ start
		{"bogus=1", "bogus"},                                             // unknown param (UNIT-014)
	}
	for _, c := range bad {
		f, param, msg := parseLogsFilter(mustValues(c.query))
		if param != c.param {
			t.Fatalf("%q: param=%q msg=%q, want param=%q", c.query, param, msg, c.param)
		}
		_ = f
	}

	// 9.2-UNIT-015 — defaults applied with no params.
	f, param, _ := parseLogsFilter(mustValues(""))
	if param != "" {
		t.Fatalf("no-params must be valid, got param=%q", param)
	}
	if f.Limit != logsDefaultLimit || f.Offset != 0 || f.Start != nil || f.End != nil {
		t.Fatalf("defaults wrong: %+v", f)
	}

	// A fully-valid combo parses cleanly.
	f2, param2, _ := parseLogsFilter(mustValues(
		"limit=25&offset=10&model=qwen-max&status=server_error&is_streaming=true&" +
			"api_key_id=22222222-2222-2222-2222-222222222222&" +
			"start=2026-06-01T00:00:00Z&end=2026-06-11T00:00:00Z"))
	if param2 != "" {
		t.Fatalf("valid combo rejected: param=%q", param2)
	}
	if f2.Limit != 25 || f2.Offset != 10 || f2.Model != "qwen-max" ||
		f2.Status != StatusClassServerError || f2.IsStreaming == nil || !*f2.IsStreaming ||
		f2.APIKeyID == "" || f2.Start == nil || f2.End == nil {
		t.Fatalf("valid combo mis-parsed: %+v", f2)
	}
}

func mustValues(raw string) url.Values {
	v, err := url.ParseQuery(raw)
	if err != nil {
		panic(err)
	}
	return v
}

// 9.2-UNIT-002 (builder) — the WHERE clause ALWAYS starts with `user_id = ?`,
// with zero filters AND with every filter; the JWT sub is args[0] in both cases.
func TestLogsWhereAlwaysFenced(t *testing.T) {
	where0, args0 := buildLogsWhere("user-A", LogsFilter{Limit: 50})
	if !strings.HasPrefix(where0, "user_id = ?") {
		t.Fatalf("zero-filter WHERE must start with the fence: %q", where0)
	}
	if len(args0) != 1 || args0[0] != "user-A" {
		t.Fatalf("zero-filter args must be [user-A]: %v", args0)
	}

	streaming := true
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	whereF, argsF := buildLogsWhere("user-A", LogsFilter{
		Model: "qwen-max", Status: StatusClassServerError, IsStreaming: &streaming,
		APIKeyID: "22222222-2222-2222-2222-222222222222", Start: &start, End: &end,
	})
	if !strings.HasPrefix(whereF, "user_id = ?") {
		t.Fatalf("full-filter WHERE must start with the fence: %q", whereF)
	}
	if argsF[0] != "user-A" {
		t.Fatalf("full-filter args[0] must be the JWT sub: %v", argsF)
	}
	// Every filter dimension contributed its predicate.
	for _, frag := range []string{"model = ?", "status_code >= 500", "is_streaming = ?", "api_key_id = ?", "ts >= ?", "ts < ?"} {
		if !strings.Contains(whereF, frag) {
			t.Fatalf("full-filter WHERE missing %q: %q", frag, whereF)
		}
	}
}

// 9.2-UNIT-010 — status class → status_code range mapping (BR-RD-6).
func TestLogsStatusClassMapping(t *testing.T) {
	cases := map[StatusClass]string{
		StatusClassSuccess:     "status_code < 400",
		StatusClassClientError: "status_code BETWEEN 400 AND 499",
		StatusClassServerError: "status_code >= 500",
	}
	for class, frag := range cases {
		where, _ := buildLogsWhere("u", LogsFilter{Status: class})
		if !strings.Contains(where, frag) {
			t.Fatalf("status=%s must map to %q: %q", class, frag, where)
		}
	}
	// No status → no status_code predicate.
	where, _ := buildLogsWhere("u", LogsFilter{})
	if strings.Contains(where, "status_code") {
		t.Fatalf("absent status must not add a status_code clause: %q", where)
	}
}

// 9.2-UNIT-003 (SQL layer) / 9.2-INT-003 mirror — the SELECT projects ONLY the
// BR-RD-9 columns and ORDER BY ts DESC, he_request_id DESC; PII/ops/cost columns
// never appear in the generated SQL.
func TestLogsSelectProjectionAndOrder(t *testing.T) {
	sql, _ := buildLogsSelect("user-A", LogsFilter{Limit: 50})
	for _, banned := range []string{"client_ip", "client_country", "user_agent", "error_message", "cost_usd", "routing_strategy"} {
		if strings.Contains(sql, banned) {
			t.Fatalf("SELECT leaks non-BR-RD-9 column %q: %s", banned, sql)
		}
	}
	if !strings.Contains(sql, "ORDER BY ts DESC, he_request_id DESC") {
		t.Fatalf("SELECT must order most-recent-first with a stable tie-break: %s", sql)
	}
	if !strings.Contains(sql, "LIMIT ? OFFSET ?") {
		t.Fatalf("SELECT must be LIMIT/OFFSET-paged: %s", sql)
	}
	// The count query carries the identical fence.
	cq, cargs := buildLogsCount("user-A", LogsFilter{})
	if !strings.Contains(cq, "WHERE user_id = ?") || !reflect.DeepEqual(cargs, []any{"user-A"}) {
		t.Fatalf("count query must be IDOR-fenced: %s args=%v", cq, cargs)
	}
}
