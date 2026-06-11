// safety-log-retention — Story 8.5 AC2 §9.3 "保留 6 个月" retention CronJob
// entrypoint. Deletes he_api.content_safety_logs rows older than 6 months in a
// single bounded range-DELETE (backed by idx_safety_logs_created_at). The cutoff
// is strict `< NOW() - INTERVAL '6 months'`, so a row exactly at the boundary is
// KEPT and the sweep is naturally idempotent (a re-run within the window deletes
// 0). It does NOT touch any user row — the §9.3 GDPR-erasure exemption lives in
// the table's deliberate absence of a users FK (migration 0014 / BR-2.2), not in
// this binary.
//
// This is a DEDICATED `main` package binary (Architect H-1 non-server precedent;
// mirrors auth-svc/cmd/monthly-cost-reset + billing-svc/cmd/monthly-invoice). It
// pulls in NO HTTP-server stack — just a PG pool. A CronJob Job runs as a SINGLE
// pod with concurrencyPolicy: Forbid, so overlapping sweeps cannot race.
//
// Env:
//
//	HE_API_DB_POSTGRES_URI — required (PG DSN; the same pool the gateway uses)
//
// Exit codes: 0 on success AND on an empty sweep (0 rows). 1 ONLY on a real
// infrastructure failure (bad config, PG connect, or the DELETE) — the CronJob
// runs with backoffLimit: 0 so a non-zero exit is human-verified (security-
// sensitive-cron posture).
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	obs "github.com/he-api/he-api/packages/go-observability"
)

// runDeadline bounds the whole job. A single index-backed range delete is fast;
// this is a generous safety margin (the Helm activeDeadlineSeconds is set above
// it).
const runDeadline = 5 * time.Minute

// retentionSQL is the §9.3 "保留 6 个月" sweep. Strict `<` cutoff (half-open) so a
// row exactly at the boundary is retained (8.5-BLIND-BOUNDARY-004); idempotent by
// construction (BR-2.5).
const retentionSQL = `DELETE FROM he_api.content_safety_logs
	WHERE created_at < NOW() - INTERVAL '6 months'`

// execer is the minimal pgx surface the sweep needs (satisfied by *pgxpool.Pool
// and pgxmock).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)
	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()
	os.Exit(run(ctx, logger))
}

// run wires the pool then delegates to sweep. Factored from main so the
// orchestration is unit-testable; sweep itself is tested directly with pgxmock.
func run(ctx context.Context, logger *slog.Logger) int {
	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		logger.Error("safety_log: retention config error", slog.String("error", "HE_API_DB_POSTGRES_URI unset"))
		return 1
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		logger.Error("safety_log: retention pg connect failed", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()
	return sweep(ctx, logger, pool)
}

// sweep runs the bounded retention DELETE and reports the structured exit code.
// 0 on success/empty, 1 on infra failure (BR-2.4). NEVER deletes rows younger
// than the cutoff; NEVER touches users (no FK).
func sweep(ctx context.Context, logger *slog.Logger, db execer) int {
	tag, err := db.Exec(ctx, retentionSQL)
	if err != nil {
		logger.Error("safety_log: retention failed", slog.String("error", err.Error()))
		return 1
	}
	if cerr := ctx.Err(); errors.Is(cerr, context.DeadlineExceeded) {
		logger.Error("safety_log: retention timeout")
		return 1
	}
	logger.Info("safety_log: retention swept rows", slog.Int64("rows_deleted", tag.RowsAffected()))
	return 0
}
