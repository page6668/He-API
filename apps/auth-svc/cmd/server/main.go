// auth-svc — He-API authentication service (Story 2.2).
//
// Internal-only Connect/gRPC service (Wright Round 1 Q1 ruling). Exposes the
// five AuthService RPCs. P2f wires RegisterUser end-to-end; VerifyEmail /
// ResendVerification (P3) and LoginUser / RefreshToken (P4) remain
// CodeUnimplemented stubs with phase pointers in the handler.
//
// Env config (all from K8s Secrets / ConfigMap mounted by infra/helm/auth-svc):
//
//   HE_API_DB_POSTGRES_URI       — Story 1.6 K8s Secret he-api-db-creds
//   HE_API_DB_REDIS_URI          — Story 1.6 K8s Secret he-api-db-creds
//   HE_API_NOTIFICATION_SVC_URL  — notification-svc ClusterIP base URL
//                                  (e.g. http://notification-svc:8080)
//   HE_API_CONSOLE_BASE_URL      — public console URL used to build the
//                                  verification email link
//                                  (e.g. https://console.he-api.com)
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
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	authjwt "github.com/he-api/he-api/apps/auth-svc/internal/jwt"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/password"
)

const (
	serviceName    = "auth-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
	listenAddr     = ":8080"

	defaultNotificationSvcURL = "http://notification-svc:8080"
	defaultConsoleBaseURL     = "http://localhost:3000"
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

	// === PG pool ============================================================
	pgURI := os.Getenv("HE_API_DB_POSTGRES_URI")
	if pgURI == "" {
		logger.Error("HE_API_DB_POSTGRES_URI is required")
		os.Exit(1)
	}
	pgCfg, err := pgxpool.ParseConfig(pgURI)
	if err != nil {
		logger.Error("parse postgres uri", slog.String("error", err.Error()))
		os.Exit(1)
	}
	// Conservative pool sizing — auth-svc handlers are short (≤300ms), so
	// 20 connections × replicas covers typical burst load. Operators can
	// tune via the URI's `pool_max_conns` query param.
	pgPool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		logger.Error("connect postgres", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer pgPool.Close()
	if err := pgPool.Ping(ctx); err != nil {
		logger.Error("postgres ping failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// === Redis client =======================================================
	redisURI := os.Getenv("HE_API_DB_REDIS_URI")
	if redisURI == "" {
		logger.Error("HE_API_DB_REDIS_URI is required")
		os.Exit(1)
	}
	redisOpt, err := redis.ParseURL(redisURI)
	if err != nil {
		logger.Error("parse redis uri", slog.String("error", err.Error()))
		os.Exit(1)
	}
	rdb := redis.NewClient(redisOpt)
	defer func() { _ = rdb.Close() }()
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Error("redis ping failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// === HIBP client ========================================================
	hibp := password.NewHIBPClient()

	// === notification-svc client ============================================
	notifURL := envOr("HE_API_NOTIFICATION_SVC_URL", defaultNotificationSvcURL)
	notifClient := notification.NewClient(http.DefaultClient, notifURL)

	// === audit publisher (NoOpPublisher until P5/T4 swaps in Kafka) =========
	auditPub := audit.NewNoOpPublisher(logger)

	// === JWT signer (Story 2.2 T0.5 K8s Secret he-api-auth-jwt-keys) ========
	// The private + public PEMs are mounted as files under
	// /etc/auth-svc/keys/{private_key.pem, public_key.pem} per the Helm
	// chart deployment.yaml. Operator-overridable for local dev via env.
	jwtPrivPath := envOr("HE_API_JWT_PRIVATE_KEY_PATH", "/etc/auth-svc/keys/private_key.pem")
	jwtPrivPEM, err := os.ReadFile(jwtPrivPath)
	if err != nil {
		logger.Error("read JWT private key", slog.String("path", jwtPrivPath), slog.String("error", err.Error()))
		os.Exit(1)
	}
	jwtSigner, err := authjwt.NewSigner(jwtPrivPEM)
	if err != nil {
		logger.Error("parse JWT private key", slog.String("error", err.Error()))
		os.Exit(1)
	}
	// Verifier from the matching public-key PEM (mounted alongside the
	// private key from the same K8s Secret per Helm chart).
	jwtPubPath := envOr("HE_API_JWT_PUBLIC_KEY_PATH", "/etc/auth-svc/keys/public_key.pem")
	jwtPubPEM, err := os.ReadFile(jwtPubPath)
	if err != nil {
		logger.Error("read JWT public key", slog.String("path", jwtPubPath), slog.String("error", err.Error()))
		os.Exit(1)
	}
	jwtVerifier, err := authjwt.NewVerifier(jwtPubPEM)
	if err != nil {
		logger.Error("parse JWT public key", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if jwtSigner.KeyID() != jwtVerifier.KeyID() {
		logger.Error("JWT key mismatch — Signer kid ≠ Verifier kid (keypair drift)",
			slog.String("signer_kid", jwtSigner.KeyID()),
			slog.String("verifier_kid", jwtVerifier.KeyID()),
		)
		os.Exit(1)
	}

	// === AuthServer =========================================================
	authServer := handlers.NewAuthServer(handlers.AuthServer{
		DB:             pgPool,
		Redis:          rdb,
		HIBP:           hibp,
		Notification:   notifClient,
		Audit:          auditPub,
		JWT:            jwtSigner,
		JWTVerify:      jwtVerifier,
		Clock:          time.Now,
		ConsoleBaseURL: envOr("HE_API_CONSOLE_BASE_URL", defaultConsoleBaseURL),
		Logger:         logger,
	})

	mux := http.NewServeMux()
	mux.Handle(authv1connect.NewAuthServiceHandler(authServer))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(mux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("auth-svc listening",
			slog.String("addr", listenAddr),
			slog.String("notification_svc_url", notifURL),
			slog.String("console_base_url", authServer.ConsoleBaseURL),
		)
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

// envOr returns the value of the named env var or fallback when unset/empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
