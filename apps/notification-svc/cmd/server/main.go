// notification-svc — He-API notification fan-out service (Story 2.2).
//
// Wright Round 1 Q4 ruling (option c, with caveat): synchronous SendEmail
// gRPC entry from auth-svc. Async Kafka migration is an Epic-9 follow-up.
//
// P1 scaffold: the binary compiles and listens, but SendEmail returns
// CodeUnimplemented. Template loader + SendGrid client + per-locale rendering
// land alongside auth-svc T1 in P2.
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

	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/apps/notification-svc/internal/handlers"
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

	mux := http.NewServeMux()
	mux.Handle(notificationv1connect.NewNotificationServiceHandler(handlers.NewNotificationServer(sender)))

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
