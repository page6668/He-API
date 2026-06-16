// account-deletion-sweeper — Story 2.7 AC6 K8s CronJob entrypoint (schedule
// `0 2 * * *` UTC). For each user whose 30-day grace has elapsed
// (status='pending_deletion' AND pending_deletion_at <= NOW()) it soft-deletes
// (anonymizes) the users row, EXPLICITLY deletes the PII children (CASCADE does
// NOT fire on the UPDATE — BR-6.3), detaches + deletes the Stripe payment
// methods (OQ-3 OVERRIDE), anonymizes ClickHouse request_logs + purges OSS
// objects, then closes the reconcile gate (anonymized_at) and emits the HIGH
// audit + completion email. Crash-safe: a row left status='deleted' AND
// anonymized_at IS NULL is re-swept for the cross-store steps only (BR-6.6).
//
// Env (reuses the auth-svc Deployment's secret):
//
//	HE_API_DB_POSTGRES_URI       — required (PG DSN)
//	HE_API_AUDIT_KAFKA_BROKERS   — optional (audit emit; graceful-degrade)
//	HE_API_NOTIFICATION_SVC_URL  — optional (completion email; graceful-degrade)
//	DRY_RUN=1                    — optional (scan + log only; no writes)
//
// Exit codes: 0 = all due users fully erased (or empty); 1 = ≥1 user failed
// (rolled back / left for reconcile) or PG/scan failure or the deadline
// (backoffLimit=0 → on-call investigates). One bad user never blocks the batch.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/deletion"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// runDeadline bounds the whole job. activeDeadlineSeconds in the CronJob is set
// above this so the binary's own ctx deadline trips first (clean exit 1).
const runDeadline = 5 * time.Minute

type config struct {
	pgURI           string
	kafkaBrokers    []string
	notificationURL string
	clickhouseDSN   string
	stripeSecretKey string
	stripeBaseURL   string
	dryRun          bool
}

func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		pgURI:           strings.TrimSpace(getenv("HE_API_DB_POSTGRES_URI")),
		notificationURL: strings.TrimSpace(getenv("HE_API_NOTIFICATION_SVC_URL")),
		clickhouseDSN:   strings.TrimSpace(getenv("HE_API_CLICKHOUSE_DSN")),
		stripeSecretKey: strings.TrimSpace(getenv("STRIPE_SECRET_KEY")),
		stripeBaseURL:   strings.TrimSpace(getenv("STRIPE_API_BASE_URL")),
		dryRun:          strings.TrimSpace(getenv("DRY_RUN")) == "1",
	}
	if brokers := strings.TrimSpace(getenv("HE_API_AUDIT_KAFKA_BROKERS")); brokers != "" {
		c.kafkaBrokers = splitNonEmpty(brokers)
	}
	if c.pgURI == "" {
		return c, errors.New("pg_dsn_missing")
	}
	return c, nil
}

func splitNonEmpty(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("account_deletion_sweeper_config_invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()

	if code := run(ctx, logger, cfg); code != 0 {
		os.Exit(code)
	}
}

// run executes one sweep. Returns the process exit code (0 success). Factored
// out of main so the orchestration stays unit-testable.
func run(ctx context.Context, logger *slog.Logger, cfg config) int {
	started := time.Now()

	pool, err := pgxpool.New(ctx, cfg.pgURI)
	if err != nil {
		logger.Error("account_deletion_sweeper_pg_connect_failed", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()

	if cfg.dryRun {
		due, derr := repository.SelectDueDeletions(ctx, pool)
		if derr != nil {
			logger.Error("account_deletion_sweeper_dry_run_scan_failed", slog.String("error", derr.Error()))
			return 1
		}
		logger.Info("account_deletion_sweeper_dry_run", slog.Int("due_users", len(due)))
		return 0
	}

	// Audit publisher (best-effort; empty brokers → NoOp graceful-degrade).
	var auditPub audit.Publisher = audit.NewNoOpPublisher(logger)
	if len(cfg.kafkaBrokers) > 0 {
		writer := &kafka.Writer{
			Addr:         kafka.TCP(cfg.kafkaBrokers...),
			Topic:        "audit.event",
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireOne,
		}
		defer func() { _ = writer.Close() }()
		auditPub = audit.NewKafkaPublisher(writer, logger)
	}

	// Completion-email mailer (best-effort; empty URL → no email).
	var mailer notification.Sender
	if cfg.notificationURL != "" {
		mailer = notification.NewClient(&http.Client{Timeout: 10 * time.Second}, cfg.notificationURL)
	}

	// Cross-store seams (AC6 steps 5/6 + OQ-3). Each is constructed from its own
	// env; an unconfigured (nil) seam SKIPS its step and the BR-6.6 reconcile
	// gate (anonymized_at IS NULL) re-sweeps that row once a later deploy wires
	// it. ClickHouse + Stripe have concrete adapters; OSS purge is deferred
	// platform-wide (the 2.6 OSS export-upload is itself not yet wired — there
	// is nothing to purge until OSS lands), so its seam stays nil here.
	var chSeam deletion.ClickHouseAnonymizer
	if cfg.clickhouseDSN != "" {
		ch, closeCH, cherr := deletion.NewClickHouseAdapter(ctx, cfg.clickhouseDSN)
		if cherr != nil {
			logger.Error("account_deletion_sweeper_clickhouse_connect_failed", slog.String("error", cherr.Error()))
			return 1
		}
		defer func() { _ = closeCH() }()
		chSeam = ch
	} else {
		logger.Warn("account_deletion_sweeper_clickhouse_unconfigured")
	}

	var stripeSeam deletion.StripeDetacher
	if cfg.stripeSecretKey != "" {
		stripeSeam = deletion.NewStripeAdapter(cfg.stripeSecretKey, cfg.stripeBaseURL)
	} else {
		logger.Warn("account_deletion_sweeper_stripe_unconfigured")
	}

	if mailer == nil {
		logger.Warn("account_deletion_sweeper_mailer_unconfigured")
	}
	logger.Warn("account_deletion_sweeper_oss_purge_deferred",
		slog.String("detail", "OSS purge skipped — platform-wide OSS not yet wired (2.6 upload deferred); reconcile gate covers when it lands"))

	d := deletion.Deps{
		DB:     pool,
		Tx:     pool,
		CH:     chSeam,
		Stripe: stripeSeam,
		Mailer: mailer,
		Audit:  auditPub,
		Logger: logger,
		Clock:  time.Now,
	}

	res, err := deletion.Run(ctx, d)
	if err != nil {
		logger.Error("account_deletion_sweeper_run_failed", slog.String("error", err.Error()))
		return 1
	}

	if cerr := ctx.Err(); errors.Is(cerr, context.DeadlineExceeded) {
		logger.Error("account_deletion_sweeper_timeout")
		return 1
	}

	logger.Info("account_deletion_sweeper_complete",
		slog.Float64("total_runtime_seconds", time.Since(started).Seconds()),
		slog.Int("processed", res.Processed),
		slog.Int("failed", res.Failed))

	if res.Failed > 0 {
		return 1 // ops-visible (BR-6.5/6.7) — one bad row fails the run but the rest committed
	}
	return 0
}
