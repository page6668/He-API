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

	"github.com/he-api/he-api/apps/billing-svc/internal/autorecharge"
	"github.com/he-api/he-api/apps/billing-svc/internal/consumer"
	"github.com/he-api/he-api/apps/billing-svc/internal/credit"
	billinggrpc "github.com/he-api/he-api/apps/billing-svc/internal/grpc"
	"github.com/he-api/he-api/apps/billing-svc/internal/ledger"
	"github.com/he-api/he-api/apps/billing-svc/internal/lowbalance"
	"github.com/he-api/he-api/apps/billing-svc/internal/paymentclient"
	"github.com/he-api/he-api/apps/billing-svc/internal/paymentmethod"
	"github.com/he-api/he-api/apps/billing-svc/internal/pricing"
	"github.com/he-api/he-api/apps/billing-svc/internal/server"
	"github.com/he-api/he-api/apps/billing-svc/internal/subscription"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
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
	obs.SetupPropagation() // Story 9.4 BR-TR-1 — global W3C propagator (extract incoming traceparent)
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

	// Story 7.7 — the post-deduction auto-recharge + low-balance hook (off the chat
	// hot path). nil when PG is unavailable.
	postDeduct := buildPostDeduction(logger, pool, rdb)

	// Kafka consumer (usage.recorded) → ledger.Apply. Wired only when both PG and
	// Kafka brokers are configured.
	stopConsumer := startConsumer(ctx, logger, pool, rdb, pricer, postDeduct)
	defer stopConsumer()

	// Story 7.3 — payment.completed consumer → credit.Apply (exactly-once balance
	// CREDIT + subscription lifecycle). Wired only when PG + Kafka are configured.
	stopCredit := startCreditConsumer(ctx, logger, pool, rdb)
	defer stopCredit()

	// gRPC handler — CheckBalance reads PG authoritatively. With no pool it falls
	// back to Unimplemented (health still serves).
	var billingHandler = unimplementedOrReal(pool, rdb, logger)

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
func unimplementedOrReal(pool *pgxpool.Pool, rdb *redis.Client, logger *slog.Logger) billingv1connect.BillingServiceHandler {
	if pool == nil {
		return nil
	}
	srv := billinggrpc.NewServer(pool, logger)

	// Story 7.8 — wire the subscription-tier surface. The PG reader resolves the
	// caller's active plan (else free); the catalogue is the entitlement SoT; the
	// snapshot writer is billing-svc's SOLE write of the gateway entitlement cache
	// (BR-E-3). ChangePlan additionally needs the payment-svc provider rail — when
	// HE_API_PAYMENT_SVC_URL is unset, GetSubscription/GetEntitlements still serve
	// (read-only) and ChangePlan returns Unimplemented.
	cat := plancatalogue.DefaultCatalogue
	subReader := subscription.NewPGSubReader(pool)
	var snapRedis subscription.Redis
	if rdb != nil {
		snapRedis = rdb
	}
	snapshot := subscription.NewSnapshotWriter(snapRedis, logger)
	if url := os.Getenv("HE_API_PAYMENT_SVC_URL"); url != "" {
		// Story 9.4 (TRACE-ORPHAN-001): instrumented client so the billing-svc→payment-svc
		// connect hop injects `traceparent` and stays on the originating trace (BR-TR-2).
		client := paymentv1connect.NewPaymentServiceClient(obs.NewHTTPClient(), url)
		updater := paymentclient.NewSubscriptionUpdater(client)
		svc := subscription.NewService(cat, subReader, updater, snapshot, logger)
		srv.SetSubscriptions(svc, subReader, cat)
	} else {
		logger.Warn("HE_API_PAYMENT_SVC_URL unset — ChangePlan disabled (GetSubscription/GetEntitlements still serve)")
		srv.SetSubscriptions(nil, subReader, cat)
	}
	return srv
}

// startConsumer wires the Kafka reader + DLQ writer + ledger and runs the
// consume loop in a goroutine. Returns a stop func (closes the reader/writer).
// A no-op when PG, Redis-optional, pricing, or Kafka brokers are unavailable.
func startConsumer(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client, pricer *pricing.Provider, postDeduct func(context.Context, string, string)) func() {
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
	if postDeduct != nil {
		l.SetPostDeduction(postDeduct) // Story 7.7 auto-recharge + alert trigger
	}
	c := consumer.New(reader, dlq, l, logger)
	go c.Run(ctx)
	logger.Info("billing consumer started", slog.String("topic", consumer.Topic))

	return func() {
		_ = reader.Close()
		_ = dlq.Close()
	}
}

// startCreditConsumer wires the payment.completed Kafka reader + the credit
// applier and runs the consume loop in a goroutine. Returns a stop func. A no-op
// when PG or Kafka brokers are unavailable.
func startCreditConsumer(ctx context.Context, logger *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client) func() {
	noop := func() {}
	if pool == nil {
		logger.Warn("credit consumer disabled — PG unavailable")
		return noop
	}
	brokersEnv := os.Getenv("HE_API_KAFKA_BROKERS")
	if brokersEnv == "" {
		logger.Warn("credit consumer disabled — HE_API_KAFKA_BROKERS unset")
		return noop
	}
	brokers := strings.Split(brokersEnv, ",")
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: credit.ConsumerGroup,
		Topic:   credit.Topic,
	})
	applier := credit.New(pool, creditRedisOrNil(rdb), logger)
	// Story 7.8 — on a confirmed subscription webhook, refresh/invalidate the
	// gateway entitlement snapshot (BR-E-3) + flip subscriptions.plan (BR-S-3).
	if rdb != nil {
		applier.SetEntitlementSnapshot(subscription.NewSnapshotWriter(rdb, logger))
	}
	c := credit.NewConsumer(reader, applier, logger)
	go c.Run(ctx)
	logger.Info("billing credit consumer started", slog.String("topic", credit.Topic))
	return func() { _ = reader.Close() }
}

// buildPostDeduction assembles the Story-7.7 auto-recharge trigger + low-balance
// alerter and returns the hook the ledger calls after each deduction commits.
// Returns nil when PG is unavailable. Degrades gracefully: with no payment-svc URL
// the charge is skipped (alerts still fire); with no notifier the alert email is
// skipped (the trigger + dedupe still run).
func buildPostDeduction(logger *slog.Logger, pool *pgxpool.Pool, rdb *redis.Client) func(context.Context, string, string) {
	if pool == nil {
		return nil
	}
	pmStore := paymentmethod.New(pool)

	var charger autorecharge.Charger
	if url := os.Getenv("HE_API_PAYMENT_SVC_URL"); url != "" {
		// Story 9.4 (TRACE-ORPHAN-001): instrumented client so the billing-svc→payment-svc
		// connect hop injects `traceparent` and stays on the originating trace (BR-TR-2).
		client := paymentv1connect.NewPaymentServiceClient(obs.NewHTTPClient(), url)
		charger = paymentclient.NewCharger(client)
	} else {
		logger.Warn("HE_API_PAYMENT_SVC_URL unset — auto-recharge off-session charge disabled (low-balance alerts still fire)")
	}

	trigger := autorecharge.New(pool, autorechargeRedis(rdb), pmStore, charger,
		os.Getenv("HE_API_LOW_BALANCE_THRESHOLD_USD"), logger)
	alerter := lowbalance.New(lowbalanceRedis(rdb), buildLowBalanceNotifier(logger), logger)

	return func(ctx context.Context, userID, newBalance string) {
		res := trigger.Evaluate(ctx, userID, newBalance)
		alerter.Handle(ctx, userID, res)
	}
}

// buildLowBalanceNotifier returns the email-delivery adapter for low-balance
// alerts, or nil when notification delivery is not yet configured. NOTE: the
// localized-render + SendGrid dispatch needs a notification-svc path that resolves
// the user (email/locale/display_name by user_id) — wired when that RPC lands; the
// alert DECISION + once-per-episode dedupe (internal/lowbalance) are independent
// of delivery and fully active.
func buildLowBalanceNotifier(logger *slog.Logger) lowbalance.Notifier {
	logger.Warn("low-balance email delivery not configured — alert decision/dedupe active, email pending notification-svc user-lookup RPC")
	return nil
}

// autorechargeRedis / lowbalanceRedis adapt a possibly-nil *redis.Client to the
// respective seams (typed-nil avoidance — a nil *redis.Client in an interface is
// non-nil, so return the interface nil explicitly).
func autorechargeRedis(rdb *redis.Client) autorecharge.Locker {
	if rdb == nil {
		return nil
	}
	return rdb
}

func lowbalanceRedis(rdb *redis.Client) lowbalance.Redis {
	if rdb == nil {
		return nil
	}
	return rdb
}

// creditRedisOrNil adapts a possibly-nil *redis.Client to the credit.Redis seam
// (typed-nil avoidance, as with redisOrNil).
func creditRedisOrNil(rdb *redis.Client) credit.Redis {
	if rdb == nil {
		return nil
	}
	return rdb
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
