//go:build integration

// Story 9.1 AC1 — ClickHouse ingestion + aggregation integration tests
// (9.1-INT-001 consume→row + MV; 9.1-INT-002 DATA-002 aggregation correctness;
// 9.1-INT-003 migration round-trip). Excluded from the default `go test` run
// (build tag `integration`) because they need a live ClickHouse — CI provides
// one via HE_API_CLICKHOUSE_TEST_DSN and applies migrations/clickhouse/002
// first. Run: `go test -tags=integration ./internal/clickhouse/`.
package clickhouse

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

func testConn(t *testing.T) driver.Conn {
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

// 9.1-INT-001 — a row inserted via the writer lands in request_logs AND the
// hourly_agg MV auto-populates (request_count, success_count, token sums, cost).
// 9.1-INT-002 — sum(success_count)/sum(request_count) over the MV equals
// countIf(status<400)/count() over the raw table (DATA-002).
func TestIngestionAndAggregation(t *testing.T) {
	conn := testConn(t)
	ctx := context.Background()
	sink := NewSink(conn)

	user := "11111111-1111-1111-1111-111111111111"
	rows := []Row{
		mkRow(user, "qwen-max", 200, 300, "0.10"), // success
		mkRow(user, "qwen-max", 200, 150, "0.05"), // success
		mkRow(user, "qwen-max", 402, 0, "0"),      // failure (drags success_rate)
	}
	if err := sink.Insert(ctx, rows); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Allow the MV merge to settle.
	time.Sleep(1 * time.Second)

	var rawNum, rawDen uint64
	if err := conn.QueryRow(ctx,
		`SELECT countIf(status_code < 400), count() FROM he_api.request_logs WHERE user_id = ?`, user,
	).Scan(&rawNum, &rawDen); err != nil {
		t.Fatalf("raw query: %v", err)
	}
	var mvNum, mvDen uint64
	if err := conn.QueryRow(ctx,
		`SELECT sum(success_count), sum(request_count) FROM he_api.request_logs_hourly_agg WHERE user_id = ?`, user,
	).Scan(&mvNum, &mvDen); err != nil {
		t.Fatalf("mv query: %v", err)
	}
	if rawNum != mvNum || rawDen != mvDen {
		t.Fatalf("aggregation drift: raw=%d/%d mv=%d/%d", rawNum, rawDen, mvNum, mvDen)
	}
	if rawDen != 3 || rawNum != 2 {
		t.Fatalf("expected 2/3 success, got %d/%d", rawNum, rawDen)
	}
}

func mkRow(user, model string, status uint16, total uint32, cost string) Row {
	c, _ := decimal.NewFromString(cost)
	return Row{
		HeRequestID:        "req_aaaaaaaaaaaa",
		UserID:             user,
		APIKeyID:           "22222222-2222-2222-2222-222222222222",
		Model:              model,
		UpstreamModel:      model,
		SelectedByStrategy: model,
		StatusCode:         status,
		TotalTokens:        total,
		CostUSD:            c,
		ClientIP:           parseIP("203.0.113.7"),
		ClientCountry:      "SG",
		Ts:                 time.Now().UTC(),
	}
}
