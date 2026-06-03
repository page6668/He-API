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

	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/routing-svc/internal/catalogue"
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

	srv, err := server.New(server.Options{
		Catalogue:   cat,
		Strategies:  strategy.DefaultStrategies(),
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

// envOr returns the value of the named env var or fallback when unset/empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
