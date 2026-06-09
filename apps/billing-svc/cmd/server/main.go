// billing-svc — He-API billing engine (Epic 7, Story 7.1).
//
// Two responsibilities:
//   - a Kafka CONSUMER of `usage.recorded` that computes cost (internal/pricing,
//     Decimal end-to-end) and applies an exactly-once balance deduction
//     (internal/ledger: one PG tx with usage_ledger ON CONFLICT + balances
//     UPSERT, then a best-effort Redis mirror);
//   - a Connect-RPC server exposing BillingService.CheckBalance (PG-authoritative
//     balance read for console / reconciliation).
//
// Observability (TracerProvider + Prometheus meter + slog JSON + /metrics) is
// REUSED from packages/go-observability (Story 1.5). slog discipline is
// non-secret: he_request_id / user_id / model / cost only (user_id is the
// billing subject — permitted, per the request_logs.user_id precedent).
//
// Env config:
//
//	PORT                     — listen port (default 8080)
//	HE_API_DB_POSTGRES_URI   — PG DSN (pricing snapshot + ledger + CheckBalance)
//	HE_API_REDIS_URL         — Redis URL (realtime mirror + month counters)
//	HE_API_KAFKA_BROKERS     — comma-separated Kafka brokers (consumer + DLQ)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/billing-svc/internal/consumer"
	billinggrpc "github.com/he-api/he-api/apps/billing-svc/internal/grpc"
	"github.com/he-api/he-api/apps/billing-svc/internal/ledger"
	"github.com/he-api/he-api/apps/billing-svc/internal/pricing"
	"github.com/he-api/he-api/apps/billing-svc/internal/server"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
)

const (
	serviceName    = "billing-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, ":"+envOr("PORT", "8080")); err != nil {
		logger.Error("billing-svc boot failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, addr string) error {
	tp, err := obs.NewTracerProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetTracerProvider(tp)
	defer shutdown(tp.Shutdown)

	mp, err := obs.NewMeterProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetMeterProvider(mp)
	defer shutdown(mp.Shutdown)

	// PG pool — required for pricing + ledger + CheckBalance. billing-svc still
	// boots without it (serving health + an Unimplemented CheckBalance) so a
	// transient PG outage at boot does not CrashLoop; the consumer is simply not
	// started until PG is configured.
	pool := buildPGPool(ctx, logger)
	if pool != nil {
		defer pool.Close()
	}

	rdb := buildRedis(logger)
	if rdb != nil {
		defer func() { _ = rdb.Close() }()
	}

	// Pricing snapshot (boot + 60s refresh) over the read pool.
	var pricer *pricing.Provider
	if pool != nil {
		boot, berr := pricing.Load(ctx, pool)
		if berr != nil {
			logger.Warn("pricing boot load failed — starting empty, refresh will recover",
				slog.String("error", berr.Error()))
			boot = pricing.NewSnapshot(nil)
		} else {
			logger.Info("pricing snapshot loaded", slog.Int("models_priced", boot.Len()))
		}
		pricer = pricing.NewProvider(boot, func(c context.Context) (*pricing.Snapshot, error) {
			return pricing.Load(c, pool)
		}, pricing.DefaultRefreshInterval, logger)
		go pricer.Run(ctx)
	}

	// Kafka consumer (usage.recorded) → ledger.Apply. Wired only when both PG and
	// Kafka brokers are configured.
	stopConsumer := startConsumer(ctx, logger, pool, rdb, pricer)
	defer stopConsumer()

	// gRPC handler — CheckBalance reads PG authoritatively. With no pool it falls
	// back to Unimplemented (health still serves).
	var billingHandler = unimplementedOrReal(pool, logger)

	srv := server.New(server.Options{
		BillingHandler: billingHandler,
		Logger:         logger,
		ServiceName:    serviceName,
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("billing-svc listening", slog.String("addr", addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
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
	return httpServer.Shutdown(shutdownCtx)
}

// unimplementedOrReal returns the real CheckBalance handler when a pool exists,
// else interface-nil (server.New falls back to the Unimplemented stub). Returning
// the interface type — not a typed-nil *Server — keeps the nil check in server.New
// correct.
func unimplementedOrReal(pool *pgxpool.Pool, logger *slog.Logger) billingv1connect.BillingServiceHandler {
	if pool == nil {
		return nil
	}
	return billinggrpc.NewServer(pool, logger)
}

// startConsumer wires the Kafka reader + DLQ writer + ledger and runs the
// consume loop in a goroutine. Returns a stop func (closes the reader/writer).
// A no-op when PG, Redis-optional, pricing, or Kafka brokers are unavailable.
func startConsumer(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client, pricer *pricing.Provider) func() {
	noop := func() {}
	if pool == nil || pricer == nil {
		logger.Warn("consumer disabled — PG/pricing unavailable")
		return noop
	}
	brokersEnv := os.Getenv("HE_API_KAFKA_BROKERS")
	if brokersEnv == "" {
		logger.Warn("consumer disabled — HE_API_KAFKA_BROKERS unset")
		return noop
	}
	brokers := strings.Split(brokersEnv, ",")

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: consumer.ConsumerGroup,
		Topic:   consumer.Topic,
	})
	// DLQ writer — acks=all is unnecessary for the DLQ itself, but RequireOne is
	// fine; the money-path producer (acks=all) is the gateway's responsibility.
	dlq := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        consumer.DLQTopic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireAll,
	}

	l := ledger.New(pool, redisOrNil(rdb), pricer, logger, ledger.NewMetrics())
	c := consumer.New(reader, dlq, l, logger)
	go c.Run(ctx)
	logger.Info("billing consumer started", slog.String("topic", consumer.Topic))

	return func() {
		_ = reader.Close()
		_ = dlq.Close()
	}
}

// redisOrNil adapts a possibly-nil *redis.Client to the ledger.Redis seam
// (typed-nil avoidance: a nil *redis.Client wrapped in the interface is non-nil,
// so return the interface nil explicitly).
func redisOrNil(rdb *redis.Client) ledger.Redis {
	if rdb == nil {
		return nil
	}
	return rdb
}

func buildPGPool(ctx context.Context, logger *slog.Logger) *pgxpool.Pool {
	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		logger.Warn("HE_API_DB_POSTGRES_URI unset — billing degraded (no deduction, CheckBalance unimplemented)")
		return nil
	}
	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		logger.Error("parse postgres uri — billing degraded", slog.String("error", err.Error()))
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		logger.Error("create postgres pool — billing degraded", slog.String("error", err.Error()))
		return nil
	}
	return pool
}

func buildRedis(logger *slog.Logger) *redis.Client {
	url := os.Getenv("HE_API_REDIS_URL")
	if url == "" {
		logger.Warn("HE_API_REDIS_URL unset — Redis mirror disabled (PG charge still durable)")
		return nil
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		logger.Error("parse redis url — Redis mirror disabled", slog.String("error", err.Error()))
		return nil
	}
	return redis.NewClient(opt)
}

func shutdown(fn func(context.Context) error) {
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = fn(sctx)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
