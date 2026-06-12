// notification-svc — He-API notification fan-out service (Story 2.2 + 2.6).
//
// Wright Round 1 Q4 ruling (option c, with caveat): synchronous SendEmail
// gRPC entry from auth-svc. Async Kafka migration is an Epic-9 follow-up.
//
// Story 2.6 — extends the binding with RequestDataExport + GetCurrentExport
// RPCs. The new RPCs require PG (data_export_requests table) + Redis (rate-
// limit safety net) + Kafka (gdpr.export.requested topic). Each wiring
// reads env vars and falls back gracefully so legacy Story-2.2 deployments
// can run without the new infrastructure (SendEmail-only mode).
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

	"github.com/he-api/he-api/apps/notification-svc/internal/audit"
	"github.com/he-api/he-api/apps/notification-svc/internal/authsvcclient"
	"github.com/he-api/he-api/apps/notification-svc/internal/events"
	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
	gdprratelimit "github.com/he-api/he-api/apps/notification-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"
	"github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1/usagelogv1connect"

	"go.opentelemetry.io/otel"
)

const (
	serviceName    = "notification-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
	listenAddr     = ":8080"
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tp, err := obs.NewTracerProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		logger.Error("tracer provider init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	otel.SetTracerProvider(tp)
	obs.SetupPropagation() // Story 9.4 BR-TR-1 — global W3C propagator (extract incoming traceparent)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = tp.Shutdown(sctx)
	}()

	// SendGrid client wires SENDGRID_API_KEY from K8s Secret he-api-notification-creds
	// (TS-CONS-010). The sender address defaults are pinned for the staging
	// sub-account; future stories add env-overridable values when prod ships.
	apiKey := os.Getenv("SENDGRID_API_KEY")
	if apiKey == "" {
		logger.Warn("SENDGRID_API_KEY unset — SendEmail will fail with 401 until the Secret is mounted")
	}
	sender := sendgrid.NewClient(apiKey, sendgrid.Address{
		Email: "noreply@he-api.com",
		Name:  "He-API",
	})

	// Story 2.6 — optional wiring for the data-export RPCs. When the
	// required env vars are missing we keep the legacy SendEmail-only
	// binding so existing Story 2.2 deployments aren't broken; the new
	// RPCs return Unimplemented in that mode (the embedded
	// *DataExportServer is nil → promoted methods panic; we guard with a
	// nil-check by binding only the full server when all deps are
	// present).
	var dataExport *handlers.DataExportServer
	var usageLogExport *handlers.UsageLogExportServer
	var capServer *handlers.CapThresholdServer
	if dbURI := strings.TrimSpace(os.Getenv("HE_API_DB_POSTGRES_URI")); dbURI != "" {
		var pool *pgxpool.Pool
		pool, err = pgxpool.New(ctx, dbURI)
		if err != nil {
			logger.Error("postgres pool init failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
		defer pool.Close()

		redisAddr := strings.TrimSpace(os.Getenv("HE_API_REDIS_ADDR"))
		if redisAddr == "" {
			logger.Error("HE_API_REDIS_ADDR required when HE_API_DB_POSTGRES_URI is set (Story 2.6 wiring)")
			os.Exit(1)
		}
		rdb := redis.NewClient(&redis.Options{
			Addr:     redisAddr,
			Password: os.Getenv("HE_API_REDIS_PASSWORD"),
		})
		defer rdb.Close()

		kafkaBrokers := strings.Split(strings.TrimSpace(os.Getenv("HE_API_KAFKA_BROKERS")), ",")
		if len(kafkaBrokers) == 1 && kafkaBrokers[0] == "" {
			logger.Error("HE_API_KAFKA_BROKERS required when HE_API_DB_POSTGRES_URI is set (Story 2.6 wiring)")
			os.Exit(1)
		}
		gdprWriter := &kafka.Writer{
			Addr:         kafka.TCP(kafkaBrokers...),
			Topic:        events.TopicGDPRExportRequested,
			Balancer:     &kafka.Hash{},
			Async:        false, // synchronous so commit-then-publish failures surface to caller
			RequiredAcks: kafka.RequireAll,
		}
		defer gdprWriter.Close()

		auditWriter := &kafka.Writer{
			Addr:         kafka.TCP(kafkaBrokers...),
			Topic:        "audit.event",
			Balancer:     &kafka.Hash{},
			Async:        true,
			RequiredAcks: kafka.RequireOne,
			Completion: func(messages []kafka.Message, err error) {
				if err != nil {
					logger.Warn("audit publish failed", slog.String("error", err.Error()), slog.Int("messages", len(messages)))
				}
			},
		}
		defer auditWriter.Close()

		dataExport = handlers.NewDataExportServer(
			handlers.PoolAdapter(pool),
			gdprratelimit.NewGDPRExportLimiter(rdb),
			events.NewGDPRExportPublisher(gdprWriter, logger),
			audit.NewKafkaPublisher(auditWriter, logger),
			logger,
		)
		logger.Info("notification-svc Story-2.6 data-export RPCs enabled")

		// Story 9.3 — usage-log export RPCs (AC1). Reuse the same pool/redis/
		// audit; a SEPARATE Kafka writer for the new topic + a SEPARATE Redis
		// limiter key namespace (BR-EX-5) so usage-log exports do not consume
		// the GDPR quota.
		usageLogWriter := &kafka.Writer{
			Addr:         kafka.TCP(kafkaBrokers...),
			Topic:        events.TopicUsageLogExportRequested,
			Balancer:     &kafka.Hash{},
			Async:        false,
			RequiredAcks: kafka.RequireAll,
		}
		defer usageLogWriter.Close()

		usageLogExport = handlers.NewUsageLogExportServer(
			handlers.PoolAdapter(pool),
			gdprratelimit.NewUsageLogExportLimiter(rdb),
			events.NewUsageLogExportPublisher(usageLogWriter, logger),
			audit.NewKafkaPublisher(auditWriter, logger),
			logger,
		)
		logger.Info("notification-svc Story-9.3 usage-log export RPCs enabled")

		// Story 5.4 — cap-threshold notification RPC. Needs the same Redis
		// (dedupe sentinels) + the auth-svc gRPC endpoint (Q-L Fix-A context
		// lookup). Env var follows the repo's HE_API_*_URL convention.
		if authURL := strings.TrimSpace(os.Getenv("HE_API_AUTH_SVC_URL")); authURL != "" {
			authClient := authsvcclient.New(obs.NewHTTPClient(obs.WithTimeout(10*time.Second)), authURL) // Story 9.4 BR-TR-2
			capServer = handlers.NewCapThresholdServer(handlers.NewRedisDedupeStore(rdb), authClient, sender, logger)
			logger.Info("notification-svc Story-5.4 cap-threshold RPC enabled")
		} else {
			logger.Warn("HE_API_AUTH_SVC_URL unset — NotifyMonthlyCapThreshold will return Unimplemented")
		}
	} else {
		logger.Warn("HE_API_DB_POSTGRES_URI unset — Story 2.6 RequestDataExport / GetCurrentExport will return Unimplemented")
	}

	mux := http.NewServeMux()
	var ns *handlers.NotificationServer
	if dataExport != nil {
		ns = handlers.NewNotificationServerWithDataExport(sender, dataExport)
	} else {
		ns = handlers.NewNotificationServer(sender)
	}
	ns.Cap = capServer // nil → NotifyMonthlyCapThreshold returns Unimplemented
	mux.Handle(notificationv1connect.NewNotificationServiceHandler(ns))

	// Story 9.3 — mount the UsageLogExportService (separate connect service).
	// When the PG block above was skipped (no HE_API_DB_POSTGRES_URI) the
	// handler is the Unimplemented stub so the route 404s cleanly rather than
	// nil-panicking.
	if usageLogExport != nil {
		mux.Handle(usagelogv1connect.NewUsageLogExportServiceHandler(usageLogExport))
	} else {
		mux.Handle(usagelogv1connect.NewUsageLogExportServiceHandler(usagelogv1connect.UnimplementedUsageLogExportServiceHandler{}))
		logger.Warn("HE_API_DB_POSTGRES_URI unset — Story 9.3 usage-log export RPCs will return Unimplemented")
	}

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("notification-svc listening", slog.String("addr", listenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serverErr:
		logger.Error("http server error", slog.String("error", err.Error()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown failed", slog.String("error", err.Error()))
	}
}
