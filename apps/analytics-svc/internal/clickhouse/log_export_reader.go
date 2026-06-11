// log_export_reader.go — Story 9.3 AC2 ClickHouse READ path for the usage-log
// export dumper (the 9.1 chsink.go is write-only; this is the paired reader).
//
// IDOR fence (BR-EX-1): every query is `WHERE user_id = ?` — the export job can
// only ever read the requesting user's rows. The projection reuses the canonical
// 13-column non-PII set (dumps.LogRowSelectColumns), byte-identical to 9.2's
// logsSelectColumns, so the export and the on-screen viewer cannot drift.
//
// Not locally runnable (no docker/testcontainers — [[project_toolchain_env_limits]]);
// exercised by the T4.1 CI integration job. Compile-clean here.
package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

// OpenConn dials ClickHouse and returns the raw driver.Conn for read queries.
// The caller owns the lifecycle (close fn). Mirrors Open but exposes the conn
// rather than wrapping it in a write Sink.
func OpenConn(ctx context.Context, dsn string) (driver.Conn, func() error, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("parse clickhouse dsn: %w", err)
	}
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("open clickhouse: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("ping clickhouse: %w", err)
	}
	return conn, conn.Close, nil
}

// LogExportReader implements dumps.LogRowFetcher over a ClickHouse conn.
type LogExportReader struct {
	conn driver.Conn
}

// NewLogExportReader wraps a driver.Conn (from OpenConn or a testcontainers conn).
func NewLogExportReader(conn driver.Conn) *LogExportReader {
	return &LogExportReader{conn: conn}
}

// logExportQuery is the IDOR-fenced, range-bounded SELECT over request_logs.
const logExportQuery = "SELECT " + dumps.LogRowSelectColumns +
	" FROM " + Table +
	" WHERE user_id = ? AND ts BETWEEN ? AND ?" +
	" ORDER BY ts DESC, he_request_id DESC"

// FetchLogRows returns the user's request_logs rows over [start, end], fenced
// `WHERE user_id = ?` (BR-EX-1). Projects ONLY the 13 BR-RD-9 non-PII columns.
func (r *LogExportReader) FetchLogRows(ctx context.Context, userID string, start, end time.Time) ([]dumps.LogRow, error) {
	rows, err := r.conn.Query(ctx, logExportQuery, userID, start, end)
	if err != nil {
		return nil, fmt.Errorf("request_logs export query: %w", err)
	}
	defer rows.Close()

	var out []dumps.LogRow
	for rows.Next() {
		var (
			heRequestID                                 string
			ts                                          time.Time
			model, upstreamModel, apiKeyID, errorCode   string
			statusCode                                  uint16
			isStreaming                                 uint8
			promptTokens, completionTokens, totalTokens uint32
			latencyMsTotal, ttfbMs                      uint32
		)
		if err := rows.Scan(
			&heRequestID, &ts, &model, &upstreamModel, &statusCode,
			&isStreaming, &promptTokens, &completionTokens, &totalTokens,
			&latencyMsTotal, &ttfbMs, &apiKeyID, &errorCode,
		); err != nil {
			return nil, fmt.Errorf("request_logs export scan: %w", err)
		}
		out = append(out, dumps.LogRow{
			HeRequestID:      heRequestID,
			Ts:               ts,
			Model:            model,
			UpstreamModel:    upstreamModel,
			StatusCode:       int(statusCode),
			IsStreaming:      isStreaming != 0,
			PromptTokens:     int64(promptTokens),
			CompletionTokens: int64(completionTokens),
			TotalTokens:      int64(totalTokens),
			LatencyMsTotal:   int64(latencyMsTotal),
			TtfbMs:           int64(ttfbMs),
			ApiKeyID:         apiKeyID,
			ErrorCode:        errorCode,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("request_logs export rows: %w", err)
	}
	return out, nil
}
