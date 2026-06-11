package clickhouse

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// chSink is the clickhouse-go/v2 binding for Sink. It is the ONLY driver-coupled
// type in the package; everything else (row mapping, batching) is driver-free
// and unit-tested. Integration tests (testcontainers ClickHouse) exercise this.
type chSink struct {
	conn driver.Conn
}

// Open dials ClickHouse from a DSN (e.g. clickhouse://user:pass@host:9000/he_api)
// and returns a Sink. The caller owns the lifecycle (Close on shutdown).
func Open(ctx context.Context, dsn string) (Sink, func() error, error) {
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
	return &chSink{conn: conn}, conn.Close, nil
}

// NewSink wraps an existing driver.Conn (used by integration tests that own the
// testcontainers connection).
func NewSink(conn driver.Conn) Sink {
	return &chSink{conn: conn}
}

// Insert prepares a named-column batch and appends every row in column order,
// then sends. A non-nil error leaves the BatchWriter buffer intact (no offset
// commit → redelivery).
func (s *chSink) Insert(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := s.conn.PrepareBatch(ctx, InsertStatement)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for i := range rows {
		if err := batch.Append(rows[i].appendArgs()...); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("append row %d: %w", i, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send batch: %w", err)
	}
	return nil
}
