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

	// Story 2.4 — JWT verifier for the protected 2FA endpoints (T1.3).
	// Parses the same RSA public key the JWKS endpoint advertises.
	rsaPub, err := parseRSAPublicPEM(jwtPubPEM)
	if err != nil {
		logger.Error("parse RSA public key for JWT verify", slog.String("error", err.Error()))
		os.Exit(1)
	}
	jwtVerifier := middleware.NewJWTVerifier(rsaPub)

	mux := http.NewServeMux()
	// Story 2.2 — /v1/auth/* REST surface (Wright Round 1 Q1 ruling).
	mux.HandleFunc("POST /v1/auth/signup", auth.Signup)
	mux.HandleFunc("GET /v1/auth/verify-email", auth.VerifyEmail)
	mux.HandleFunc("POST /v1/auth/resend-verification", auth.ResendVerification)
	mux.HandleFunc("POST /v1/auth/signin", auth.Signin)
	mux.HandleFunc("POST /v1/auth/refresh", auth.Refresh)
	mux.HandleFunc("GET /.well-known/jwks.json", jwks.Serve)

	// Story 2.3 — OAuth surface (4 endpoints). Provider parameterised in the
	// path; OAuthHandler dispatches on the literal segment so the upstream
	// gRPC call receives the right Provider string.
	oauthHandler := &handlers.OAuthHandler{Upstream: authUpstream, Env: deployEnv}
	// Per BR-4.1 ratelimit each endpoint on its own counter. Redis client
	// shares the same backend as Story 2.2's ratelimit.
	// NOTE: P6 wires the route topology; the actual Redis client is wired
	// in cmd/server/main.go once HE_API_REDIS_URL surfaces in env (P7 task).
	// For now, route un-rate-limited — CI gate confirms the routes are
	// registered; ratelimit middleware is functional and unit-tested.
	mux.Handle("GET /v1/auth/oauth/google/initiate", oauthHandler.Initiate("google"))
	mux.Handle("GET /v1/auth/oauth/google/callback", oauthHandler.Callback("google"))
	mux.Handle("GET /v1/auth/oauth/github/initiate", oauthHandler.Initiate("github"))
	mux.Handle("GET /v1/auth/oauth/github/callback", oauthHandler.Callback("github"))

	// Story 2.4 — TOTP 2FA routes (T1.3, T2.5, T3.3, T4.3 wire the rest).
	// JWT-protected: /v1/auth/2fa/enroll/* and (T4.3) /v1/auth/2fa/disable +
	// (T3.3) /v1/auth/2fa/recovery-codes/regenerate. The two challenge
	// endpoints (/v1/auth/2fa/challenge + /v1/auth/2fa/recovery-codes/use)
	// use he_mfa cookie instead — wired in T2.5 / T3.3.
	mux.Handle("POST /v1/auth/2fa/enroll/init", jwtVerifier.RequireJWT(http.HandlerFunc(auth.EnrollTOTPInit)))
	mux.Handle("POST /v1/auth/2fa/enroll/verify", jwtVerifier.RequireJWT(http.HandlerFunc(auth.EnrollTOTPVerify)))
	// /v1/auth/2fa/challenge uses he_mfa cookie (not he_access), so it is
	// NOT wrapped by jwtVerifier.RequireJWT — auth-svc verifies the
	// mfa_token internally and short-circuits on missing JTI / binding fail.
	mux.HandleFunc("POST /v1/auth/2fa/challenge", auth.ChallengeTOTP)
	// /recovery-codes/use also uses he_mfa cookie; /regenerate is JWT-protected.
	// AAL=2 enforcement on regenerate lands in T5.2 — for now T1.3 RequireJWT
	// gives us authenticated-only.
	mux.HandleFunc("POST /v1/auth/2fa/recovery-codes/use", auth.UseRecoveryCode)
	// Regenerate + disable require aal=2 (Story 2.4 BR-4.1 / T5.2). The
	// outer RequireJWT places the AAL claim in context; the inner RequireAAL
	// gates on min=2 → emits 403_aal2_required when the user only signed in
	// with password.
	mux.Handle("POST /v1/auth/2fa/recovery-codes/regenerate",
		jwtVerifier.RequireJWT(jwtVerifier.RequireAAL(2, http.HandlerFunc(auth.RegenerateRecoveryCodes))))
	mux.Handle("POST /v1/auth/2fa/disable",
		jwtVerifier.RequireJWT(jwtVerifier.RequireAAL(2, http.HandlerFunc(auth.DisableTOTP))))

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

// parseRSAPublicPEM parses an RSA public key from PEM bytes. Used by
// middleware.JWTVerifier (Story 2.4 T1.3). Returns an error if the PEM is
// malformed or the key is not RSA.
func parseRSAPublicPEM(pemBytes []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("rsa: no PEM block")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("rsa: parse public key: %w", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("rsa: public key is not RSA")
	}
	return pub, nil
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
