package analyticsquery

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

// costMoneyScale is the NUMERIC(12,4) usage_ledger money scale.
const costMoneyScale int32 = 4

// chStore is the clickhouse-go/v2 reader. It reads via a "read-only" pool — this
// is app-level DISCIPLINE, not a DB-enforced boundary (M-2): the 001-baseline
// he_api user holds SELECT, INSERT, so the reader simply issues no writes. A
// dedicated he_api_ro GRANT-SELECT-only user is the recorded defence-in-depth
// follow-up. Q-CHCLIENT: the gateway reads ClickHouse DIRECTLY (fewer hops).
type chStore struct {
	conn driver.Conn
}

// OpenStore dials ClickHouse from a DSN and returns a read-only-by-discipline
// Store plus its closer. HE_API_CLICKHOUSE_DSN unset → caller disables the route.
func OpenStore(ctx context.Context, dsn string) (Store, func() error, error) {
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
	return &chStore{conn: conn}, conn.Close, nil
}

// NewStore wraps an existing driver.Conn (integration tests).
func NewStore(conn driver.Conn) Store { return &chStore{conn: conn} }

// The dashboard volume metrics (请求数 / 成功率 / Token) come from ClickHouse;
// 消费 (cost) does NOT — it is read from PG usage_ledger (the billed-money SoT,
// H-1-R / R2-2). request_logs.cost_usd stays in the schema but is non-authoritative
// (always 0, no gateway pricing seam — OQ5), so these read queries deliberately do
// NOT select it.

// todayQuery reads request_logs DIRECTLY (Q-RT — realtime; the MV lags on merge).
// IDOR fence: user_id = ? is always bound to the JWT sub.
const todayQuery = `SELECT count(), countIf(status_code < 400), ` +
	`sum(prompt_tokens), sum(completion_tokens), sum(total_tokens) ` +
	`FROM he_api.request_logs WHERE user_id = ? AND ts >= ?`

// aggQuery reads the hourly_agg MV (month/quarter — cheap over wide windows).
const aggQuery = `SELECT sum(request_count), sum(success_count), ` +
	`sum(prompt_tokens), sum(completion_tokens), sum(total_tokens) ` +
	`FROM he_api.request_logs_hourly_agg WHERE user_id = ? AND hour >= ?`

func (s *chStore) Today(ctx context.Context, userID string, since time.Time) (PeriodAgg, error) {
	return s.scan(ctx, todayQuery, userID, since)
}

func (s *chStore) AggSince(ctx context.Context, userID string, since time.Time) (PeriodAgg, error) {
	return s.scan(ctx, aggQuery, userID, since)
}

func (s *chStore) scan(ctx context.Context, query, userID string, since time.Time) (PeriodAgg, error) {
	var (
		requests, success         uint64
		prompt, completion, total uint64
	)
	row := s.conn.QueryRow(ctx, query, userID, since)
	if err := row.Scan(&requests, &success, &prompt, &completion, &total); err != nil {
		return PeriodAgg{}, err
	}
	return PeriodAgg{
		Requests:         int64(requests),
		Success:          int64(success),
		PromptTokens:     int64(prompt),
		CompletionTokens: int64(completion),
		TotalTokens:      int64(total),
	}, nil
}

// CostStore is the PG usage_ledger read surface for the 消费 metric (H-1-R).
// 消费 is the billed-money SoT — computed once by billing-svc and persisted in
// he_api.usage_ledger — so the dashboard READS it here rather than recomputing a
// second number that could disagree with the user's invoice. CostSince MUST
// parameterize user_id = $sub (IDOR fence — R2-6, identical to the CH reads).
type CostStore interface {
	// CostSince sums usage_ledger.cost_usd for the user since the period start.
	// The same user-tz period bound used for the ClickHouse query is passed here
	// (one bounds computation, two stores, identical windows — R2-1).
	CostSince(ctx context.Context, userID string, since time.Time) (decimal.Decimal, error)
}

// pgCostStore runs the usage_ledger SUM over the EXISTING gateway pgxpool (the
// same pool billing_read.go uses — R2-6, no new PG connection path).
type pgCostStore struct {
	db RowQuerier
}

// NewCostStore wraps the gateway PG pool as a CostStore. A nil db yields nil so
// the handler degrades 消费 to null (R2-4) rather than panicking.
func NewCostStore(db RowQuerier) CostStore {
	if db == nil {
		return nil
	}
	return &pgCostStore{db: db}
}

// costSumSQL is the billed-money SUM, IDOR-fenced and bounded by the same period
// start computed for ClickHouse. COALESCE(...,0) so a no-rows period reads as 0
// (mirrors billing_read.go usageTotalsSQL).
const costSumSQL = `SELECT COALESCE(SUM(cost_usd),0)::text ` +
	`FROM he_api.usage_ledger WHERE user_id = $1 AND ts >= $2`

func (s *pgCostStore) CostSince(ctx context.Context, userID string, since time.Time) (decimal.Decimal, error) {
	var raw string
	if err := s.db.QueryRow(ctx, costSumSQL, userID, since).Scan(&raw); err != nil {
		return decimal.Zero, err
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		// A NUMERIC column always parses; defensive only.
		return decimal.Zero, nil
	}
	return d, nil
}
