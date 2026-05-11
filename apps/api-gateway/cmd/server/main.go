// Package main is the He-API gateway entrypoint.
//
// Wright Round 1 Q1 ruling: console BFF → api-gateway REST → auth-svc gRPC.
// This binary owns the public-facing /v1/auth/* routes + the JWKS endpoint;
// downstream Epic 3+ /v1/chat/completions land in this same mux.
//
// P1 scaffold: routes are wired, but each auth handler returns 501 with a
// pointer to the phase that materializes it. Real upstream gRPC client wiring,
// rate-limit middleware integration, JWT verify middleware activation, and
// Set-Cookie translation land in P2-P5.
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
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"

	"go.opentelemetry.io/otel"
)

const (
	serviceName    = "api-gateway"
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

	auth := handlers.NewAuthProxy()
	jwks := handlers.NewJWKSHandler()

	mux := http.NewServeMux()
	// Story 2.2 — /v1/auth/* REST surface (Wright Round 1 Q1 ruling).
	mux.HandleFunc("POST /v1/auth/signup", auth.Signup)
	mux.HandleFunc("GET /v1/auth/verify-email", auth.VerifyEmail)
	mux.HandleFunc("POST /v1/auth/resend-verification", auth.ResendVerification)
	mux.HandleFunc("POST /v1/auth/signin", auth.Signin)
	mux.HandleFunc("POST /v1/auth/refresh", auth.Refresh)
	mux.HandleFunc("GET /.well-known/jwks.json", jwks.Serve)

	// Middleware chain: security_headers (always on) → csrf (state-mutating
	// POSTs only — chained inside handlers per Q1 ruling) → jwt_verify (only
	// on /v1/protected/* routes, materializes Story 2.5+; P1 holds the stub).
	handler := middleware.SecurityHeaders(mux)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(handler, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("api-gateway listening", slog.String("addr", listenAddr))
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
