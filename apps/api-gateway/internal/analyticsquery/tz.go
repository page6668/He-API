package analyticsquery

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// RowQuerier is the minimal pgx surface the timezone resolver needs (satisfied
// by *pgxpool.Pool; tests inject a fake). Mirrors handlers.BillingReadQuerier.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewPGTimezoneResolver resolves users.timezone (Q-TZ) over the gateway PG pool.
// It is fail-safe: any error / unknown tz string / nil pool → UTC, so a tz
// lookup can never fail the dashboard read. Whole-hour offsets align cleanly
// with the UTC-hour MV buckets; sub-hour offsets approximate at the hour edge
// (documented acceptable).
func NewPGTimezoneResolver(db RowQuerier, logger *slog.Logger) TimezoneResolver {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, userID string) *time.Location {
		if db == nil {
			return time.UTC
		}
		var tz string
		err := db.QueryRow(ctx, "SELECT timezone FROM he_api.users WHERE id = $1", userID).Scan(&tz)
		if err != nil || tz == "" {
			return time.UTC
		}
		loc, lerr := time.LoadLocation(tz)
		if lerr != nil {
			logger.WarnContext(ctx, "usage_summary_unknown_timezone", slog.String("timezone", tz))
			return time.UTC
		}
		return loc
	}
}
