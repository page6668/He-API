//go:build integration

// Story 9.1b AC1 — ClickHouse integration tests for GET /v1/me/usage/series.
// These prove the SQL-correctness scenarios a fakeStore cannot: the P0 IDOR fence
// at the WHERE-clause level (9.1b-INT-005), the hourly_agg→day rollup tying to
// raw request_logs rows (9.1b-INT-007 / BLIND-DATA-001), By Status reading
// request_logs status classes (9.1b-INT-003), and timezone day bucketing
// (9.1b-INT-004). Excluded from the default `go test` run (build tag
// `integration`) — they need a live ClickHouse with migration 002 applied, which
// CI's clickhouse-integration job provides via HE_API_CLICKHOUSE_TEST_DSN. Run:
//
//	go test -tags=integration ./apps/api-gateway/internal/analyticsquery/
//
// Reuses testLogsConn / seedLogs / seedRow from logs_integration_test.go (same
// package + build tag). Isolated user namespace so it never collides with the
// /logs or analytics-svc writer suites against the shared CH instance.
package analyticsquery

import (
	"context"
	"testing"
	"time"
)

const (
	seriesUserA = "5e51e500-0000-0000-0000-00000000000a"
	seriesUserB = "5e51e500-0000-0000-0000-00000000000b"
	seriesKeyA  = "5e51e500-1111-1111-1111-11111111111a"
	seriesKeyB  = "5e51e500-1111-1111-1111-11111111111b"
)

// sumRequests totals the requests across all buckets (rollup-vs-raw assertions).
func sumRequests(points []SeriesPoint) int64 {
	var n int64
	for _, p := range points {
		n += p.Requests
	}
	return n
}

// 9.1b-INT-005 — IDOR fence at the SQL layer: user A's /series query returns ONLY
// A's buckets across every grouping; B's rows never leak (WHERE user_id=$sub).
func TestSeriesCrossUserIsolation(t *testing.T) {
	conn := testLogsConn(t)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	seedLogs(t, conn, []seedRow{
		{"req_s_a0000000001", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 10, base},
		{"req_s_a0000000002", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 20, base.Add(time.Hour)},
		{"req_s_b0000000001", seriesUserB, seriesKeyB, "qwen-max", 200, 0, 99, base},
	})
	store := NewSeriesStore(conn)
	since := base.Add(-time.Hour)

	for _, gb := range []SeriesGroupBy{GroupByDay, GroupByModel, GroupByStatus} {
		points, err := store.Series(context.Background(), seriesUserA, gb, since, time.UTC)
		if err != nil {
			t.Fatalf("gb=%s: %v", gb, err)
		}
		if got := sumRequests(points); got != 2 {
			t.Fatalf("gb=%s: user A sees %d requests, want 2 (B's row must be fenced out)", gb, got)
		}
	}

	// B querying its own rows sees only its single request — never A's.
	bPoints, err := store.Series(context.Background(), seriesUserB, GroupByDay, since, time.UTC)
	if err != nil {
		t.Fatalf("B day: %v", err)
	}
	if got := sumRequests(bPoints); got != 1 {
		t.Fatalf("user B sees %d requests, want 1", got)
	}
}

// 9.1b-INT-007 / BLIND-DATA-001 — the hourly_agg day-rollup SUM ties exactly to
// the raw request_logs row count, per day and (By Model) per model.
func TestSeriesRollupTiesToRaw(t *testing.T) {
	conn := testLogsConn(t)
	day1 := time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
	seedLogs(t, conn, []seedRow{
		{"req_s_r0000000001", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 10, day1},
		{"req_s_r0000000002", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 10, day1.Add(2 * time.Hour)},
		{"req_s_r0000000003", seriesUserA, seriesKeyA, "deepseek-v3", 200, 0, 10, day1.Add(3 * time.Hour)},
		{"req_s_r0000000004", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 10, day2},
	})
	store := NewSeriesStore(conn)
	since := day1.Add(-time.Hour)

	// By Day: day1 = 3 requests, day2 = 1 request.
	day, err := store.Series(context.Background(), seriesUserA, GroupByDay, since, time.UTC)
	if err != nil {
		t.Fatalf("by day: %v", err)
	}
	byDate := map[string]int64{}
	for _, p := range day {
		if p.Key != nil {
			t.Fatalf("By Day key must be nil, got %q", *p.Key)
		}
		byDate[p.Bucket] = p.Requests
	}
	if byDate["2026-04-10"] != 3 || byDate["2026-04-11"] != 1 {
		t.Fatalf("By Day rollup mismatch: %+v", byDate)
	}

	// By Model on day1: qwen-max = 2, deepseek-v3 = 1 (ties to raw per model).
	model, err := store.Series(context.Background(), seriesUserA, GroupByModel, since, time.UTC)
	if err != nil {
		t.Fatalf("by model: %v", err)
	}
	perModel := map[string]int64{}
	for _, p := range model {
		if p.Bucket == "2026-04-10" && p.Key != nil {
			perModel[*p.Key] = p.Requests
		}
	}
	if perModel["qwen-max"] != 2 || perModel["deepseek-v3"] != 1 {
		t.Fatalf("By Model rollup mismatch on 2026-04-10: %+v", perModel)
	}
}

// 9.1b-INT-003 — By Status reads request_logs status classes (NOT the agg MV,
// which has no status-class column): success (<400) vs error (>=400).
func TestSeriesByStatusReadsRequestLogs(t *testing.T) {
	conn := testLogsConn(t)
	d := time.Date(2026, 4, 12, 8, 0, 0, 0, time.UTC)
	seedLogs(t, conn, []seedRow{
		{"req_s_t0000000001", seriesUserA, seriesKeyA, "qwen-max", 200, 0, 10, d},
		{"req_s_t0000000002", seriesUserA, seriesKeyA, "qwen-max", 204, 0, 10, d.Add(time.Hour)},
		{"req_s_t0000000003", seriesUserA, seriesKeyA, "qwen-max", 503, 0, 0, d.Add(2 * time.Hour)},
		{"req_s_t0000000004", seriesUserA, seriesKeyA, "qwen-max", 404, 0, 0, d.Add(3 * time.Hour)},
	})
	store := NewSeriesStore(conn)
	points, err := store.Series(context.Background(), seriesUserA, GroupByStatus, d.Add(-time.Hour), time.UTC)
	if err != nil {
		t.Fatalf("by status: %v", err)
	}
	byClass := map[string]int64{}
	for _, p := range points {
		if p.Key != nil {
			byClass[*p.Key] = p.Requests
		}
	}
	if byClass[statusKeySuccess] != 2 || byClass[statusKeyError] != 2 {
		t.Fatalf("By Status class counts wrong: %+v", byClass)
	}
}

// 9.1b-INT-004 — day bucketing honours the user's timezone: a row at 16:30Z lands
// in the 2026-06-11 bucket under Asia/Shanghai (+08), not 2026-06-10 (UTC).
func TestSeriesDayBucketTimezone(t *testing.T) {
	conn := testLogsConn(t)
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	ts := time.Date(2026, 6, 10, 16, 30, 0, 0, time.UTC) // 2026-06-11 00:30 +08
	seedLogs(t, conn, []seedRow{
		{"req_s_z0000000001", seriesUserB, seriesKeyB, "qwen-max", 200, 0, 10, ts},
	})
	store := NewSeriesStore(conn)
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	points, err := store.Series(context.Background(), seriesUserB, GroupByDay, since, sh)
	if err != nil {
		t.Fatalf("by day tz: %v", err)
	}
	var got string
	for _, p := range points {
		if p.Requests > 0 {
			got = p.Bucket
		}
	}
	if got != "2026-06-11" {
		t.Fatalf("tz bucket = %q, want 2026-06-11 (Asia/Shanghai local day)", got)
	}
}
