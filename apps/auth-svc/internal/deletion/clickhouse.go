package deletion

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// ClickHouseAdapter implements ClickHouseAnonymizer against the he_api.request_logs
// table (AC6 step 5). Anonymizes the user's rows by NULLing user_id via a
// ClickHouse mutation; the 90-day TTL still reaps the rows on schedule.
//
// ClickHouse mutations (ALTER TABLE … UPDATE) are asynchronous + eventually
// consistent — acceptable here: the GDPR guarantee is that the linkage is
// severed, and the rows expire under TTL regardless. user_id is a String column,
// so it is set to ” (empty) rather than NULL.
type ClickHouseAdapter struct {
	conn driver.Conn
}

// NewClickHouseAdapter opens a ClickHouse connection from a DSN
// (clickhouse://user:pass@host:9000/he_api) and returns the adapter plus a close
// func. Mirrors apps/analytics-svc/internal/clickhouse Open.
func NewClickHouseAdapter(ctx context.Context, dsn string) (*ClickHouseAdapter, func() error, error) {
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
	return &ClickHouseAdapter{conn: conn}, conn.Close, nil
}

// NewClickHouseAdapterFromConn wraps an existing driver.Conn (test injection).
func NewClickHouseAdapterFromConn(conn driver.Conn) *ClickHouseAdapter {
	return &ClickHouseAdapter{conn: conn}
}

// AnonymizeUser NULLs (empties) the user_id of the user's request_logs rows.
func (a *ClickHouseAdapter) AnonymizeUser(ctx context.Context, userID uuid.UUID) error {
	// Parameterised mutation — user_id is a String column.
	const stmt = "ALTER TABLE he_api.request_logs UPDATE user_id = '' WHERE user_id = ?"
	if err := a.conn.Exec(ctx, stmt, userID.String()); err != nil {
		return fmt.Errorf("clickhouse anonymize request_logs: %w", err)
	}
	return nil
}

var _ ClickHouseAnonymizer = (*ClickHouseAdapter)(nil)
