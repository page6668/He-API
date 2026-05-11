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
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"go.opentelemetry.io/otel"
)

const defaultAuthSvcURL = "http://auth-svc:8080"

// envOr returns the value of name or fallback when unset / empty.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

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

	// Connect-go client to auth-svc. HTTP/2 over the cluster Service URL;
	// auth-svc is a ClusterIP service (Wright Round 1 Q1 ruling — internal-only)
	// and the gateway is the only ingress.
	authSvcURL := envOr("HE_API_AUTH_SVC_URL", defaultAuthSvcURL)
	authUpstream := authv1connect.NewAuthServiceClient(
		&http.Client{Timeout: 10 * time.Second},
		authSvcURL,
	)
	deployEnv := handlers.ParseDeployEnv(os.Getenv("HE_API_DEPLOY_ENV"))
	auth := handlers.NewAuthProxyWithEnv(authUpstream, deployEnv)

	// JWKS — load the same public key auth-svc carries. Build the JWKS
	// document inline (the auth-svc jwt package's internal/ scope is
	// unreachable from this binary by Go's internal-package rule; a
	// future refactor can extract a shared packages/auth-jwt). The
	// construction is ~30 lines and the math is RFC-7515-canonical.
	jwtPubPath := envOr("HE_API_JWT_PUBLIC_KEY_PATH", "/etc/api-gateway/keys/public_key.pem")
	jwtPubPEM, err := os.ReadFile(jwtPubPath)
	if err != nil {
		logger.Error("read JWT public key", slog.String("path", jwtPubPath), slog.String("error", err.Error()))
		os.Exit(1)
	}
	jwksBytes, err := jwksFromPublicPEM(jwtPubPEM)
	if err != nil {
		logger.Error("build JWKS", slog.String("error", err.Error()))
		os.Exit(1)
	}
	jwks := handlers.NewJWKSHandler(jwksBytes)

	mux := http.NewServeMux()
	// Story 2.2 — /v1/auth/* REST surface (Wright Round 1 Q1 ruling).
	mux.HandleFunc("POST /v1/auth/signup", auth.Signup)
	mux.HandleFunc("GET /v1/auth/verify-email", auth.VerifyEmail)
	mux.HandleFunc("POST /v1/auth/resend-verification", auth.ResendVerification)
	mux.HandleFunc("POST /v1/auth/signin", auth.Signin)
	mux.HandleFunc("POST /v1/auth/refresh", auth.Refresh)
	mux.HandleFunc("GET /.well-known/jwks.json", jwks.Serve)

	// Middleware chain (outer → inner): SecurityHeaders → CSRF → mux.
	// SecurityHeaders writes the BR-4.7 response headers on every response.
	// CSRF rejects state-mutating POSTs without a matching Origin header
	// (BR-4.6); GET / HEAD / OPTIONS flow through. JWT-verify on protected
	// routes lands in Story 2.5+.
	csrfAllowed := csrfAllowlistFor(deployEnv)
	handler := middleware.SecurityHeaders(middleware.CSRF(middleware.CSRFConfig{
		AllowedOrigins: csrfAllowed,
	}, mux))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(handler, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("api-gateway listening",
			slog.String("addr", listenAddr),
			slog.String("auth_svc_url", authSvcURL),
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

// csrfAllowlistFor returns the Origin allowlist for the current deploy
// env. Production accepts any *.he-api.com origin; staging restricts to
// *.staging.he-api.com; development allows localhost on common dev
// ports.
func csrfAllowlistFor(env handlers.DeployEnv) []string {
	switch env {
	case handlers.EnvProduction:
		return []string{".he-api.com"}
	case handlers.EnvStaging:
		return []string{".staging.he-api.com"}
	default:
		return []string{
			"http://localhost:3000",
			"http://127.0.0.1:3000",
			"http://localhost:8080",
		}
	}
}

// jwksFromPublicPEM builds the JWKS document from an RSA public-key PEM.
// Mirrors apps/auth-svc/internal/jwt.Verifier.JWKS() — kept inline here
// because Go's internal-package rule blocks the gateway from importing
// auth-svc's jwt package directly. The math is canonical RFC 7515/7518.
func jwksFromPublicPEM(pemBytes []byte) ([]byte, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("jwks: no PEM block")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwks: parse public key: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("jwks: public key is not RSA")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("jwks: marshal: %w", err)
	}
	sum := sha256.Sum256(der)
	kid := fmt.Sprintf("%x", sum[:8])
	doc := struct {
		Keys []map[string]string `json:"keys"`
	}{
		Keys: []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	return json.Marshal(doc)
}
