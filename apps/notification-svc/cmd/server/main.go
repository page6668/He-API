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

	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/apps/notification-svc/internal/audit"
	"github.com/he-api/he-api/apps/notification-svc/internal/events"
	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
	gdprratelimit "github.com/he-api/he-api/apps/notification-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/notification-svc/internal/sendgrid"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"

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
	} else {
		logger.Warn("HE_API_DB_POSTGRES_URI unset — Story 2.6 RequestDataExport / GetCurrentExport will return Unimplemented")
	}

	mux := http.NewServeMux()
	if dataExport != nil {
		mux.Handle(notificationv1connect.NewNotificationServiceHandler(
			handlers.NewNotificationServerWithDataExport(sender, dataExport),
		))
	} else {
		mux.Handle(notificationv1connect.NewNotificationServiceHandler(
			handlers.NewNotificationServer(sender),
		))
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
