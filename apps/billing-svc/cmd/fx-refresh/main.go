// fx-refresh — Story 7.2 AC2 K8s CronJob entrypoint (schedule `0 0 * * *` UTC).
// Fetches the latest USD→CNY rate from the configured FxProvider and APPENDS a
// new he_api.fx_rates row; the gateway's display-conversion snapshot picks it up
// on its next ~60s refresh.
//
// This is a DEDICATED `main` package binary (Architect H-1 — NOT a subcommand on
// the billing-svc server; mirrors the Story-5.4 monthly-cost-reset cron). It
// pulls in NO HTTP-server stack — just the provider + a PG pool.
//
// Env:
//
//	HE_API_DB_POSTGRES_URI  — required (PG DSN; the fx_rates write target)
//	HE_API_FX_PROVIDER_URL  — FX provider base URL (NEW secret, env-injected,
//	                          NEVER logged — BR-C-7); used unless the manual
//	                          override is set
//	FX_MANUAL_USD_CNY       — optional override (dev / CI / air-gapped); bypasses
//	                          the HTTP provider
//	FX_PROVIDER_SOURCE      — optional row `source` label (default: manual|http)
//
// Exit codes (BR-C-3): 0 on success AND on STALE-SERVE (provider failure /
// non-positive rate — no row inserted, last-known row stays active, retry next
// day). 1 ONLY on a real infrastructure failure (bad config, PG connect, or the
// INSERT itself) — backoffLimit=0, so on-call investigates.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/billing-svc/internal/fx"
)

const (
	serviceName    = "fx-refresh"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

// runDeadline bounds the whole job. The provider fetch is separately bounded
// (fx.DefaultTimeout); this is the outer guard.
const runDeadline = 1 * time.Minute

// config is the validated environment for one run.
type config struct {
	pgURI    string
	source   string
	provider fx.FxProvider
}

// loadConfig reads + validates the environment and selects the provider. A
// missing PG DSN or an unconfigured/invalid provider is a fail-fast boot error
// (returns a non-nil error → exit 1), NOT a stale-serve.
func loadConfig(getenv func(string) string) (config, error) {
	pg := strings.TrimSpace(getenv("HE_API_DB_POSTGRES_URI"))
	if pg == "" {
		return config{}, errors.New("pg_dsn_missing")
	}
	provider, err := fx.ProviderFromEnv(getenv)
	if err != nil {
		return config{}, err
	}
	source := strings.TrimSpace(getenv("FX_PROVIDER_SOURCE"))
	if source == "" {
		if strings.TrimSpace(getenv("FX_MANUAL_USD_CNY")) != "" {
			source = "manual"
		} else {
			source = "http"
		}
	}
	return config{pgURI: pg, source: source, provider: provider}, nil
}

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		logger.Error("fx_refresh_config_invalid", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), runDeadline)
	defer cancel()

	os.Exit(run(ctx, logger, cfg))
}

// run executes one refresh. Returns the process exit code (0 success/stale-serve;
// 1 infra failure). Factored out of main so the orchestration is unit-testable.
func run(ctx context.Context, logger *slog.Logger, cfg config) int {
	// Best-effort meter provider (the slog is the durable signal for a short-
	// lived cron; metrics are advisory).
	if mp, err := obs.NewMeterProvider(ctx, serviceName, serviceNS, serviceVersion); err == nil {
		otel.SetMeterProvider(mp)
		defer func() {
			sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer scancel()
			_ = mp.Shutdown(sctx)
		}()
	}

	pool, err := pgxpool.New(ctx, cfg.pgURI)
	if err != nil {
		logger.Error("fx_refresh_pg_connect_failed", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()

	inserted, rerr := fx.Refresh(ctx, cfg.provider, pool, cfg.source, fx.NewMetrics(), logger, time.Now)
	if rerr != nil {
		// Real infrastructure failure (DB insert) — exit non-zero.
		return 1
	}
	if !inserted {
		// STALE-SERVE — provider failed / non-positive rate. The last-known row
		// stays active; exit 0 so the cron retries next day (BR-C-3).
		logger.Warn("fx_refresh_stale_serve",
			slog.String("event", "fx_refresh.stale_serve"),
			slog.String("note", "no row inserted; last-known rate remains active"))
	}
	return 0
}
