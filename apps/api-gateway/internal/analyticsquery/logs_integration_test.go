//go:build integration

// Story 9.2 AC1 — ClickHouse integration tests for GET /v1/me/usage/logs
// (9.2-INT-001 cross-user IDOR; 9.2-INT-002 IDOR via api_key_id; 9.2-INT-003 PII
// never SELECTed; 9.2-INT-004 deterministic ordering; 9.2-INT-005 filter
// narrowing; 9.2-INT-006 cap + total_count math). Excluded from the default
// `go test` run (build tag `integration`) — they need a live ClickHouse with
// migration 002 applied, which CI's clickhouse-integration job provides via
// HE_API_CLICKHOUSE_TEST_DSN. Run: `go test -tags=integration ./apps/api-gateway/internal/analyticsquery/`.
package analyticsquery

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Isolated user/key namespace so this suite never collides with the analytics-svc
// writer integration test running against the same CH instance.
const (
	logUserA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	logUserB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	logKeyA  = "a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1"
	logKeyB  = "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1"
)

func testLogsConn(t *testing.T) driver.Conn {
	t.Helper()
	dsn := os.Getenv("HE_API_CLICKHOUSE_TEST_DSN")
	if dsn == "" {
		t.Skip("HE_API_CLICKHOUSE_TEST_DSN unset — skipping ClickHouse integration test")
	}
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type seedRow struct {
	heReqID     string
	user        string
	apiKey      string
	model       string
	status      uint16
	isStreaming uint8
	total       uint32
	ts          time.Time
}

func seedLogs(t *testing.T, conn driver.Conn, rows []seedRow) {
	t.Helper()
	ctx := context.Background()
	batch, err := conn.PrepareBatch(ctx,
		`INSERT INTO he_api.request_logs `+
			`(he_request_id, user_id, api_key_id, model, upstream_model, status_code, `+
			`is_streaming, prompt_tokens, completion_tokens, total_tokens, `+
			`latency_ms_total, ttfb_ms, error_code, client_ip, client_country, user_agent, ts)`)
	if err != nil {
		t.Fatalf("prepare batch: %v", err)
	}
	for _, r := range rows {
		// client_ip / client_country / user_agent are seeded with PII-looking
		// values so 9.2-INT-003 can prove the read NEVER returns them.
		if err := batch.Append(
			r.heReqID, r.user, r.apiKey, r.model, r.model, r.status,
			r.isStreaming, uint32(0), uint32(0), r.total,
			uint32(0), uint32(0), "",
			"203.0.113.7", "SG", "secret-agent/1.0", r.ts,
		); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatalf("send batch: %v", err)
	}
}

// 9.2-INT-001 — cross-user isolation: user A's query returns ONLY A's rows;
// user B's rows never appear, regardless of filters.
// 9.2-INT-002 — IDOR via api_key_id: user B filtering on user A's key → empty.
func TestLogsCrossUserIsolation(t *testing.T) {
	conn := testLogsConn(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedLogs(t, conn, []seedRow{
		{"req_a00000000001", logUserA, logKeyA, "qwen-max", 200, 0, 10, now.Add(-3 * time.Minute)},
		{"req_a00000000002", logUserA, logKeyA, "qwen-max", 200, 1, 20, now.Add(-2 * time.Minute)},
		{"req_b00000000001", logUserB, logKeyB, "qwen-max", 200, 0, 99, now.Add(-1 * time.Minute)},
	})
	store := NewLogsStore(conn)

	itemsA, totalA, err := store.Logs(context.Background(), logUserA, LogsFilter{Limit: 50})
	if err != nil {
		t.Fatalf("A logs: %v", err)
	}
	if totalA != 2 {
		t.Fatalf("user A total_count=%d, want 2 (B's row excluded)", totalA)
	}
	for _, it := range itemsA {
		if it.HeRequestID == "req_b00000000001" {
			t.Fatalf("IDOR LEAK: user A saw user B's row %s", it.HeRequestID)
		}
	}

	// 9.2-INT-002 — B filters on A's api_key_id → fence yields zero.
	itemsLeak, totalLeak, err := store.Logs(context.Background(), logUserB, LogsFilter{Limit: 50, APIKeyID: logKeyA})
	if err != nil {
		t.Fatalf("B leak query: %v", err)
	}
	if totalLeak != 0 || len(itemsLeak) != 0 {
		t.Fatalf("IDOR LEAK via api_key_id: B got %d rows of A's key", len(itemsLeak))
	}
}

// 9.2-INT-003 — PII never SELECTed: the projection excludes client_ip /
// client_country / user_agent / error_message / cost_usd at the SQL layer, even
// though those columns carry data in the seeded rows.
func TestLogsProjectionExcludesPII(t *testing.T) {
	conn := testLogsConn(t)
	now := time.Now().UTC()
	seedLogs(t, conn, []seedRow{
		{"req_c00000000001", logUserA, logKeyA, "qwen-max", 200, 0, 10, now},
	})
	// A direct scan of the LogEntry projection must succeed and yield exactly the
	// BR-RD-9 columns — the column list is asserted by the build query unit test;
	// here we prove the projection round-trips with no PII column in the SELECT.
	rows, err := conn.Query(context.Background(),
		"SELECT "+logsSelectColumns+" FROM he_api.request_logs WHERE user_id = ? LIMIT 1", logUserA)
	if err != nil {
		t.Fatalf("projection query: %v", err)
	}
	defer rows.Close()
	cols := rows.Columns()
	for _, banned := range []string{"client_ip", "client_country", "user_agent", "error_message", "cost_usd"} {
		for _, c := range cols {
			if c == banned {
				t.Fatalf("projection leaked %q at the SQL layer: %v", banned, cols)
			}
		}
	}
	if len(cols) != 13 {
		t.Fatalf("projection has %d columns, want 13 (BR-RD-9): %v", len(cols), cols)
	}
}

// 9.2-INT-004 — deterministic ordering: equal-ts rows tie-break on
// he_request_id DESC (ts DESC primary).
func TestLogsDeterministicOrdering(t *testing.T) {
	conn := testLogsConn(t)
	eq := time.Now().UTC().Truncate(time.Millisecond)
	user := "dddddddd-dddd-dddd-dddd-dddddddddddd"
	seedLogs(t, conn, []seedRow{
		{"req_d00000000001", user, logKeyA, "m", 200, 0, 1, eq},
		{"req_d00000000003", user, logKeyA, "m", 200, 0, 1, eq},
		{"req_d00000000002", user, logKeyA, "m", 200, 0, 1, eq},
	})
	store := NewLogsStore(conn)
	items, _, err := store.Logs(context.Background(), user, LogsFilter{Limit: 50})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 rows, got %d", len(items))
	}
	want := []string{"req_d00000000003", "req_d00000000002", "req_d00000000001"}
	for i, w := range want {
		if items[i].HeRequestID != w {
			t.Fatalf("ordering[%d]=%s, want %s (ts DESC, he_request_id DESC)", i, items[i].HeRequestID, w)
		}
	}
}

// 9.2-INT-005 — each filter dimension narrows the set (AND-combined). Here:
// status class (server_error) + is_streaming.
func TestLogsFilterNarrowing(t *testing.T) {
	conn := testLogsConn(t)
	now := time.Now().UTC()
	user := "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	seedLogs(t, conn, []seedRow{
		{"req_e00000000001", user, logKeyA, "qwen-max", 200, 0, 1, now.Add(-4 * time.Minute)},
		{"req_e00000000002", user, logKeyA, "qwen-max", 503, 1, 1, now.Add(-3 * time.Minute)},
		{"req_e00000000003", user, logKeyA, "qwen-plus", 503, 0, 1, now.Add(-2 * time.Minute)},
		{"req_e00000000004", user, logKeyA, "qwen-max", 404, 1, 1, now.Add(-1 * time.Minute)},
	})
	store := NewLogsStore(conn)
	stream := true
	items, total, err := store.Logs(context.Background(), user, LogsFilter{
		Limit: 50, Status: StatusClassServerError, IsStreaming: &stream,
	})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	// Only req_e00000000002 is BOTH server_error AND streaming.
	if total != 1 || len(items) != 1 || items[0].HeRequestID != "req_e00000000002" {
		t.Fatalf("AND-combined filter wrong: total=%d items=%+v", total, items)
	}
}
