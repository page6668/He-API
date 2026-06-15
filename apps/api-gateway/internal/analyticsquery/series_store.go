package analyticsquery

// Story 9.1b AC1 — the usage-trend read surface over the SAME 9.1 ClickHouse
// tables (request_logs + request_logs_hourly_agg). It opens NO second ClickHouse
// path — production reuses the *chStore that OpenStore already dialled for
// /summary + /logs (BR-CH-SERIES-1). Every query is IDOR-fenced on
// user_id = $JWT.sub (BR-CH-SERIES-3): user_id is server-resolved from the JWT
// `sub` and NEVER accepted from a query param.
//
// Cost-SoT discipline (BR-CH-SERIES-2 / 9.1 H-1-R): request_logs.cost_usd is
// non-authoritative (always 0), so the trend volume queries deliberately do NOT
// select it. Under the SM default for Q-SERIES-COST there is no cost series at
// all; a usage_ledger-backed cost series (PG, never ClickHouse) would be a
// follow-up if Architect ever rules it IN.

import (
	"context"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// SeriesGroupBy is the trend grouping dimension (BR-CH-SERIES-2 / data-validation).
type SeriesGroupBy string

const (
	GroupByDay    SeriesGroupBy = "day"    // one bucket per local-day, key=nil (P-4 default)
	GroupByModel  SeriesGroupBy = "model"  // one bucket per (local-day, model)
	GroupByStatus SeriesGroupBy = "status" // one bucket per (local-day, success|error)
)

// Status-class keys for group_by=status (AC2 example: "2xx success vs 4xx/5xx
// error"). Kept semantic (not the literal "2xx"/"4xx/5xx") so the chart legend
// is i18n-mappable; the success=<400, error=>=400 split is the wire contract.
const (
	statusKeySuccess = "success" // status_code < 400 (2xx/3xx)
	statusKeyError   = "error"   // status_code >= 400 (4xx/5xx)
)

// SeriesPoint is one trend bucket. Key is nil for By Day; the model name for
// By Model; the status class ("success"/"error") for By Status. Cost is NOT a
// field here — under the SM default the trend carries no cost series, and
// request_logs.cost_usd is non-authoritative (H-1-R), so it is never read.
type SeriesPoint struct {
	Bucket      string  // local-day "YYYY-MM-DD" in the user's timezone (Q-TZ)
	Key         *string // group-dimension value; nil for By Day
	Requests    int64
	TotalTokens int64
}

// SeriesStore is the ClickHouse trend read surface. Series MUST inject
// `WHERE user_id = ?` unconditionally (BR-CH-SERIES-3) and bucket by the user's
// local day (loc). loc==nil is treated as UTC (the tz resolver already fails
// safe to UTC, so this is defensive).
type SeriesStore interface {
	Series(ctx context.Context, userID string, gb SeriesGroupBy, since time.Time, loc *time.Location) ([]SeriesPoint, error)
}

// NewSeriesStore wraps an existing driver.Conn as a SeriesStore (integration
// tests). Production reuses the OpenStore-dialled *chStore via a type assertion
// in main.go, so no second ClickHouse connection is opened (BR-CH-SERIES-1).
func NewSeriesStore(conn driver.Conn) SeriesStore { return &chStore{conn: conn} }

// The day bucket is computed in the user's timezone with toDate(t, <tz>): the
// `hour`/`ts` columns are stored UTC, so the tz name (an IANA string such as
// "Asia/Shanghai", or "UTC") is bound as the toDate timezone argument. Whole-hour
// offsets align cleanly with the UTC-hour MV buckets; sub-hour offsets approximate
// at the hour edge (documented acceptable — tz.go parity).

// By Day / By Model roll the hourly_agg MV up to the local day (cheap over wide
// windows). By Status reads request_logs raw (the MV has no status-class column).
const (
	seriesByDayQuery = `SELECT toString(toDate(hour, ?)) AS d, ` +
		`sum(request_count), sum(total_tokens) ` +
		`FROM he_api.request_logs_hourly_agg ` +
		`WHERE user_id = ? AND hour >= ? ` +
		`GROUP BY d ORDER BY d`

	seriesByModelQuery = `SELECT toString(toDate(hour, ?)) AS d, model, ` +
		`sum(request_count), sum(total_tokens) ` +
		`FROM he_api.request_logs_hourly_agg ` +
		`WHERE user_id = ? AND hour >= ? ` +
		`GROUP BY d, model ORDER BY d, model`

	seriesByStatusQuery = `SELECT toString(toDate(ts, ?)) AS d, ` +
		`if(status_code < 400, 'success', 'error') AS k, ` +
		`count(), sum(total_tokens) ` +
		`FROM he_api.request_logs ` +
		`WHERE user_id = ? AND ts >= ? ` +
		`GROUP BY d, k ORDER BY d, k`
)

func (s *chStore) Series(ctx context.Context, userID string, gb SeriesGroupBy, since time.Time, loc *time.Location) ([]SeriesPoint, error) {
	tz := "UTC"
	if loc != nil {
		tz = loc.String()
	}
	switch gb {
	case GroupByModel:
		return s.scanKeyed(ctx, seriesByModelQuery, tz, userID, since)
	case GroupByStatus:
		return s.scanKeyed(ctx, seriesByStatusQuery, tz, userID, since)
	default: // GroupByDay
		return s.scanByDay(ctx, tz, userID, since)
	}
}

// scanByDay reads the un-keyed (key=nil) day series. The connection iterator is
// released on every path (RESOURCE-001 / BR-CH-SERIES-1 pool discipline).
func (s *chStore) scanByDay(ctx context.Context, tz, userID string, since time.Time) ([]SeriesPoint, error) {
	rows, err := s.conn.Query(ctx, seriesByDayQuery, tz, userID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SeriesPoint, 0, 30)
	for rows.Next() {
		var (
			day              string
			requests, tokens uint64
		)
		if err := rows.Scan(&day, &requests, &tokens); err != nil {
			return nil, err
		}
		out = append(out, SeriesPoint{Bucket: day, Key: nil, Requests: int64(requests), TotalTokens: int64(tokens)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanKeyed reads a (day, key) series (By Model / By Status). key is carried as
// a non-nil *string so the wire emits the dimension value per BR-CH-SERIES-2.
func (s *chStore) scanKeyed(ctx context.Context, query, tz, userID string, since time.Time) ([]SeriesPoint, error) {
	rows, err := s.conn.Query(ctx, query, tz, userID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SeriesPoint, 0, 30)
	for rows.Next() {
		var (
			day, key         string
			requests, tokens uint64
		)
		if err := rows.Scan(&day, &key, &requests, &tokens); err != nil {
			return nil, err
		}
		k := key
		out = append(out, SeriesPoint{Bucket: day, Key: &k, Requests: int64(requests), TotalTokens: int64(tokens)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
