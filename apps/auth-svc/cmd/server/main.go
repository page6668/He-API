// auth-svc — He-API authentication service (Story 2.2).
//
// Internal-only gRPC service (Wright Round 1 Q1 ruling). Exposes the five
// AuthService RPCs (RegisterUser / VerifyEmail / ResendVerification /
// LoginUser / RefreshToken) on a Connect/gRPC/gRPC-Web tri-protocol handler.
//
// P1 scaffold: this binary compiles and listens, but every handler returns
// connect.CodeUnimplemented. Real handler logic lands across P2-P5 (Story
// 2.2 tasks T1-T4 per Dev Log Resumption Guide).
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
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"go.opentelemetry.io/otel"
)

const (
	serviceName    = "auth-svc"
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

	mux := http.NewServeMux()
	mux.Handle(authv1connect.NewAuthServiceHandler(handlers.NewAuthServer()))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("auth-svc listening", slog.String("addr", listenAddr))
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
