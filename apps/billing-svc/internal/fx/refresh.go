package fx

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// Execer is the minimal pgx surface the refresh INSERT needs. Satisfied by
// *pgxpool.Pool (production) and pgxmock (unit tests).
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertSQL appends a NEW rate row (BR-C-1 append-only; the active rate is the
// latest fetched_at row, read by the gateway as ORDER BY fetched_at DESC LIMIT
// 1). fetched_at = now() (DB clock). ON CONFLICT DO NOTHING guards the
// astronomically-unlikely same-instant PK collision (concurrencyPolicy: Forbid
// already prevents overlapping runs — BLIND-CONCURRENCY-001); no UPDATE-in-place
// so the audit trail is never lost (BR-C-4).
const insertSQL = `INSERT INTO he_api.fx_rates (base_currency, quote_currency, rate, source, fetched_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (base_currency, quote_currency, fetched_at) DO NOTHING`

// metricsSink records refresh outcomes. Satisfied by *Metrics (otel) and a test
// stub — keeps Refresh observable without an otel reader in unit tests (INT-031).
type metricsSink interface {
	completedInc(ctx context.Context)
	failedInc(ctx context.Context, reason string)
	recordSuccessAt(t time.Time)
}

// Refresh runs one fx-refresh: fetch USD→CNY, validate, and INSERT a new row on
// success. It returns (inserted, err):
//
//   - inserted=true,  err=nil  → a fresh row was appended (fx_refresh.completed).
//   - inserted=false, err=nil  → STALE-SERVE: the provider failed or returned a
//     non-positive/invalid rate; NOTHING was inserted, the last row stays active,
//     fx_refresh.failed fired. The cron treats this as exit 0 (retry next day,
//     BR-C-3) — a stale rate is served, a zero/null rate is NEVER persisted.
//   - inserted=false, err!=nil → a real INFRASTRUCTURE failure (the DB INSERT
//     itself failed). The cron exits non-zero (on-call investigates, backoffLimit=0).
//
// `source` labels the row (the provider id). `now` supplies the success
// timestamp for the gauge (the DB sets fetched_at; this is for observability).
func Refresh(ctx context.Context, p FxProvider, db Execer, source string, m metricsSink, logger *slog.Logger, now func() time.Time) (bool, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}

	rate, err := p.Get(ctx)
	if err != nil {
		// Provider failure → STALE-SERVE (timeout / non-2xx / malformed).
		logger.WarnContext(ctx, "fx_refresh_failed",
			slog.String("event", "fx_refresh.failed"),
			slog.String("reason", "provider_error"),
			slog.String("error", err.Error()))
		m.failedInc(ctx, "provider_error")
		return false, nil
	}
	if !rate.IsPositive() {
		// Zero / negative rate → REJECT; never persist or serve (BR-C-3 crux).
		logger.WarnContext(ctx, "fx_refresh_failed",
			slog.String("event", "fx_refresh.failed"),
			slog.String("reason", "non_positive_rate"),
			slog.String("rate", rate.String())) // rate value is public market data — loggable
		m.failedInc(ctx, "non_positive_rate")
		return false, nil
	}

	if _, err := db.Exec(ctx, insertSQL, BaseCurrency, QuoteCurrency, rate, source); err != nil {
		// The fetch+validate succeeded but the DB write failed — infrastructure
		// error; surface it so the cron exits non-zero.
		logger.ErrorContext(ctx, "fx_refresh_insert_failed",
			slog.String("event", "fx_refresh.insert_failed"),
			slog.String("error", err.Error()))
		m.failedInc(ctx, "db_error")
		return false, err
	}

	logger.InfoContext(ctx, "fx_refresh_completed",
		slog.String("event", "fx_refresh.completed"),
		slog.String("base", BaseCurrency),
		slog.String("quote", QuoteCurrency),
		slog.String("rate", rate.String()), // public market data — loggable
		slog.String("source", source))
	m.completedInc(ctx)
	m.recordSuccessAt(now().UTC())
	return true, nil
}

// validRate reports whether a rate may be persisted (finite — decimal is always
// finite — and strictly positive). NaN/Inf cannot exist as a Decimal: a provider
// that emits them fails at the json.Number→Decimal parse (ErrMalformed) before
// reaching here (UNIT-020).
func validRate(rate decimal.Decimal) bool { return rate.IsPositive() }
