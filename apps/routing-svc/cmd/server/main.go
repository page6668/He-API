// routing-svc — He-API intelligent-routing service (Epic 6, Story 6.1).
//
// FIRST routing-svc realisation: a single RoutingService.SelectModel
// Connect-RPC backed by a Strategy interface + decision engine + 4
// deterministic strategy stubs (quality / cost / latency — Q-G indistinguishable
// stubs; default — passthrough). Server-side only in 6.1 — the gateway-side
// client wiring + real scoring land in Story 6.2.
//
// Observability (TracerProvider + Prometheus meter + slog JSON + /metrics) is
// REUSED verbatim from packages/go-observability (Story 1.5 / 2.2). slog
// discipline is non-PII: strategy + model_id + he_request_id only.
//
// Env config:
//
//	PORT — listen port (default 8080)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/routing-svc/internal/catalogue"
	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/pricing"
	"github.com/he-api/he-api/apps/routing-svc/internal/server"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

const (
	serviceName    = "routing-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, catalogue.Load(), ":"+envOr("PORT", "8080")); err != nil {
		// Structured (slog JSON) fail-fast error — pod CrashLoopBackOff
		// surfaces a boot misconfiguration (UNIT-003).
		logger.Error("routing-svc boot failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run wires observability, constructs the server (fail-fast on empty catalogue
// / nil-impl slug), serves until ctx is cancelled (SIGINT/SIGTERM) or the
// listener errors, then shuts down gracefully. It returns a non-nil error only
// on a boot/serve failure — the caller maps that to a non-zero exit.
//
// The catalogue + listen address are parameters (not loaded internally) so the
// boot-validation + lifecycle paths are deterministically testable.
func run(ctx context.Context, logger *slog.Logger, cat modelscatalogue.Catalogue, addr string) error {
	// AC1 boot guard FIRST — fail fast on an empty catalogue (reusing the
	// canonical engine.NewEngine invariant) before any subsystem (observability,
	// pricing) initialises or logs, so the boot error is the sole diagnostic on
	// this path (6.1-UNIT-003). server.New re-validates as defence-in-depth.
	if _, err := engine.NewEngine(cat, nil); err != nil {
		return err
	}

	tp, err := obs.NewTracerProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetTracerProvider(tp)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = tp.Shutdown(sctx) // RESOURCE-002: flush spans, no leaked exporter goroutine
	}()

	mp, err := obs.NewMeterProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetMeterProvider(mp)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = mp.Shutdown(sctx)
	}()

	// Story 6.2 — pricing snapshot source (cost scoring + quality/latency
	// degraded fallback). Resilient: an unset DSN or a PG-unavailable boot does
	// NOT fail routing-svc (BLIND-ERROR-003) — cost degrades to first-
	// alphabetical and the 60s refresh recovers once PG is reachable. Quality/
	// latency Scorers stay nil → scoring.NoData (degraded until Epic 9, Q-A).
	deps, cleanupPricing := buildStrategyDeps(ctx, logger)
	defer cleanupPricing()

	srv, err := server.New(server.Options{
		Catalogue:   cat,
		Strategies:  strategy.DefaultStrategies(deps),
		Logger:      logger,
		ServiceName: serviceName,
	})
	if err != nil {
		return err // empty catalogue / nil-impl slug — fail-fast (AC1)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("routing-svc listening", slog.String("addr", addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// FLOW-001: the engine + catalogue are fully constructed (server.New
	// above) before readiness flips — any SelectModel accepted after /ready=200
	// is guaranteed a constructed engine.
	srv.SetReady(true)

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serverErr:
		return err
	}

	srv.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}

// buildStrategyDeps wires the pricing snapshot source from
// HE_API_DB_POSTGRES_URI (Q-K read-only pgx pool, mirroring auth-svc). It is
// deliberately resilient — routing-svc must boot + serve even when pricing is
// unavailable:
//
//   - DSN unset            → pricing disabled; cost degrades to first-alphabetical.
//   - DSN parse error      → pricing disabled (logged); same degradation.
//   - PG unreachable @boot → empty snapshot; the 60s refresh recovers (BLIND-ERROR-003).
//
// The returned cleanup closes the pool (the refresh goroutine stops on ctx
// cancel). Quality/latency Scorers are left nil → scoring.NoData (Q-A: degraded
// until Epic 9 populates ClickHouse).
func buildStrategyDeps(ctx context.Context, logger *slog.Logger) (strategy.Deps, func()) {
	noop := func() {}

	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		logger.Warn("HE_API_DB_POSTGRES_URI unset — cost routing degrades to first-alphabetical (pricing disabled)")
		return strategy.Deps{}, noop
	}

	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		logger.Error("parse postgres uri — pricing disabled", slog.String("error", err.Error()))
		return strategy.Deps{}, noop
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		logger.Error("create postgres pool — pricing disabled", slog.String("error", err.Error()))
		return strategy.Deps{}, noop
	}

	// Best-effort boot snapshot — a failure here is non-fatal (BLIND-ERROR-003).
	boot, err := pricing.Load(ctx, pool)
	if err != nil {
		logger.Warn("pricing boot load failed — starting empty, refresh will recover",
			slog.String("error", err.Error()))
		boot = pricing.NewSnapshot(nil)
	} else {
		logger.Info("pricing snapshot loaded", slog.Int("models_priced", boot.Len()))
	}

	provider := pricing.NewProvider(boot, func(c context.Context) (*pricing.Snapshot, error) {
		return pricing.Load(c, pool)
	}, pricing.DefaultRefreshInterval, logger)
	go provider.Run(ctx) // stops on ctx cancel

	return strategy.Deps{Prices: provider}, pool.Close
}

// envOr returns the value of the named env var or fallback when unset/empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
