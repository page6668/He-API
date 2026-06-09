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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/billingemit"
	"github.com/he-api/he-api/apps/api-gateway/internal/fxrate"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/billinggate"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/cors"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/notifyclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/usage"
	obs "github.com/he-api/he-api/packages/go-observability"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
)

const (
	defaultAuthSvcURL         = "http://auth-svc:8080"
	defaultNotificationSvcURL = "http://notification-svc:8080"
	defaultRedisURL           = "redis://redis:6379/0"
)

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
	// Story 3.6: wire the requestid extractor so every slog record under a
	// user-traffic request carries `he_request_id` alongside trace_id/span_id.
	logger := obs.NewLogger(slog.LevelInfo, obs.WithRequestIDExtractor(requestid.FromContext))
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

	// Story 5.3 ISSUE-006 — install the global meter provider BEFORE any
	// package constructs an OTel meter (ratelimit.New → newMetrics calls
	// otel.Meter(...); if the provider is still the no-op at that point
	// the he_ratelimit_* instruments are orphaned and never surface on
	// `/metrics`). The Prometheus exporter registers on
	// prometheus.DefaultRegisterer which obs.WrapHTTPHandler exposes.
	mp, err := obs.NewMeterProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		logger.Error("meter provider init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	otel.SetMeterProvider(mp)
	defer func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = mp.Shutdown(sctx)
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

	// Story 3.1 — /health probe handler (AC1). Constructed before the main
	// mux so it can be mounted on a sibling probeMux that bypasses the
	// SecurityHeaders + CSRF chain (BR-1.3). The `serviceVersion` const is
	// the single source of truth for the version string (BR-1.8).
	health := handlers.NewHealthHandler(serviceVersion)

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

	// Story 2.5 — Profile management routes (Architect Q4 ruling 2026-05-16:
	// aal>=1 only, NO RequireAAL(2,...) wrap — profile mutation is low-blast-
	// radius UX, not auth/billing/key surface). The api-gateway middleware
	// derives user_id from the JWT `sub` claim; the handlers NEVER trust a
	// client-supplied user_id (BR-1.1 IDOR defence).
	mux.Handle("GET /v1/me", jwtVerifier.RequireJWT(http.HandlerFunc(auth.GetMe)))
	mux.Handle("PUT /v1/me/profile", jwtVerifier.RequireJWT(http.HandlerFunc(auth.UpdateProfile)))

	// Story 5.1 — API Key management routes (AC1/AC2/AC3).
	// Wrapped by JWT-only middleware (NO bearer-auth — BR-1.1: key-can-
	// create-keys lateral movement explicitly rejected). aal>=1 per Q7 SM
	// default ratified by Architect Round 1 (revoke is a security-positive
	// action; AAL2 would create counter-productive friction).
	//
	// Origin-based CSRF (middleware.CSRF wrapping the mux at line ~280) is
	// the inherited defence for POST/DELETE; no per-route CSRF wrap needed
	// (Story 2.5 /v1/me/profile precedent).
	meKeysRedisURL := envOr("HE_API_REDIS_URL", defaultRedisURL)
	meKeys := handlers.NewMeKeysHandler(authUpstream, func() *redis.Client {
		opt, parseErr := redis.ParseURL(meKeysRedisURL)
		if parseErr != nil {
			logger.Warn(
				"me_keys redis URL parse failed — rate-limit will fail-open",
				slog.String("url", meKeysRedisURL),
				slog.String("error", parseErr.Error()),
			)
			return nil
		}
		return redis.NewClient(opt)
	}, logger)
	mux.Handle("POST /v1/me/keys", jwtVerifier.RequireJWT(http.HandlerFunc(meKeys.HandleCreate)))
	mux.Handle("GET /v1/me/keys", jwtVerifier.RequireJWT(http.HandlerFunc(meKeys.HandleList)))
	mux.Handle("DELETE /v1/me/keys/{api_key_id}", jwtVerifier.RequireJWT(http.HandlerFunc(meKeys.HandleRevoke)))
	// Story 5.2 — PATCH config (scope / ip_whitelist / monthly cost cap).
	// JWT-only (parity with the other /v1/me/keys routes); the global
	// origin-based CSRF middleware covers the PATCH mutation (BR; no per-route
	// CSRF wrap, matching POST/DELETE above).
	mux.Handle("PATCH /v1/me/keys/{api_key_id}", jwtVerifier.RequireJWT(http.HandlerFunc(meKeys.HandleUpdate)))

	// Story 2.6 — GDPR data-export routes (AC2). Proxies to notification-svc.
	// Both routes are aal>=1 (parity with Story 2.5 — user-visible
	// account-data action, not credential mutation). BR-2.4 user_id-from-JWT
	// enforcement happens inside the handler (handlers/account_data.go).
	notificationSvcURL := envOr("HE_API_NOTIFICATION_SVC_URL", defaultNotificationSvcURL)
	notificationUpstream := notificationv1connect.NewNotificationServiceClient(
		&http.Client{Timeout: 10 * time.Second},
		notificationSvcURL,
	)
	accountData := handlers.NewAccountDataProxy(notificationUpstream)
	mux.Handle("POST /v1/account/data-export",
		jwtVerifier.RequireJWT(http.HandlerFunc(accountData.RequestDataExport)))
	mux.Handle("GET /v1/account/data-export/current",
		jwtVerifier.RequireJWT(http.HandlerFunc(accountData.GetCurrentExport)))

	// Story 3.2 — Bearer-token API-key auth on the OpenAI-compatible
	// /v1/* routes (AC1 / AC4). The middleware is wrapped PER ROUTE — the
	// JWT-protected /v1/auth/*, /v1/me*, /v1/account/data-export* routes
	// remain JWT-only by design (TC-7 / BR-1.4).
	//
	// Redis client construction is lazy (BR-4.7) — the factory passed to
	// NewAPIKeyAuthenticator is invoked at most once on the first cache
	// GET, so a misconfigured HE_API_REDIS_URL does NOT prevent cold-start
	// (Story 3.1 TC-10 — cold-start ≤ 1000 ms P95 stays intact).
	redisURL := envOr("HE_API_REDIS_URL", defaultRedisURL)
	bearerAuth := middleware.NewAPIKeyAuthenticator(
		authUpstream,
		func() *redis.Client {
			opt, parseErr := redis.ParseURL(redisURL)
			if parseErr != nil {
				logger.Warn(
					"bearer_auth redis URL parse failed — cache disabled",
					slog.String("url", redisURL),
					slog.String("error", parseErr.Error()),
				)
				return nil
			}
			return redis.NewClient(opt)
		},
		logger,
	)

	// Story 5.3 — 3-axis rate-limit middleware (QPS / RPM / TPM). Loaded
	// from env vars `RATELIMIT_FREE_TIER_{QPS,RPM,TPM}_MAX` (templated by
	// Helm `infra/helm/api-gateway/values.yaml`). HALT-on-invalid keeps
	// operators from accidentally booting a gateway with `qpsMax=0` (which
	// would deny every authenticated request). Shares the bearer-auth
	// Redis URL — same backend, distinct key namespace.
	rlCeilings, rlErr := loadRateLimitCeilings()
	if rlErr != nil {
		logger.Error("ratelimit env validation failed", slog.String("error", rlErr.Error()))
		os.Exit(1)
	}
	rateLimitMW := ratelimit.New(ratelimit.Config{
		Redis:            redis.NewClient(mustRedisOptions(redisURL, logger)),
		FreeTierDefaults: rlCeilings,
		FailOpenTimeout:  ratelimit.DefaultFailOpenTimeout,
	}, logger)

	// Story 3.3 — /v1/chat/completions: non-streaming mock chat handler.
	// Replaces the Story-3.2 `chatPlaceholder` 501 stub. Bearer-auth wrap
	// is preserved (BR-1.1); the inner handler swap is the only change to
	// the route registration.
	//
	// Story 4.1 — adapterclient.Registry resolves model="deepseek-v3" to
	// the DeepSeek adapter Connect-RPC endpoint (env DEEPSEEK_ADAPTER_ENDPOINT).
	// Empty env → registry omits the entry → all models fall through to
	// the Story-3.3 mock. Stories 4.2-4.6 add sibling env-var lookups.
	adapterRegistry := adapterclient.LoadFromEnv()
	// Story 6.2 — routing decision client. ROUTING_SVC_ENDPOINT unset → nil
	// client → the Decider passes req.Model through (pre-6.2 behaviour).
	routingDecider := routingclient.NewDecider(routingclient.LoadFromEnv(), logger)

	// Story 7.1 — usage.recorded PRODUCER (Q-PRODUCER: the gateway emits, billing-
	// svc consumes). HE_API_KAFKA_BROKERS unset → Nop emitter (no emission). The
	// writer is acks=all (Q-KCLIENT — a lost charge event is unacceptable).
	var usageEmitter billingemit.UsageEmitter = billingemit.Nop{}
	if brokersEnv := os.Getenv("HE_API_KAFKA_BROKERS"); brokersEnv != "" {
		usageWriter := &kafka.Writer{
			Addr:         kafka.TCP(splitCSV(brokersEnv)...),
			Topic:        billingemit.Topic,
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll, // acks=all — money path (Q-KCLIENT)
		}
		usageEmitter = billingemit.NewKafkaEmitter(usageWriter, logger)
		logger.Info("usage.recorded producer wired", slog.String("topic", billingemit.Topic))
	} else {
		logger.Warn("HE_API_KAFKA_BROKERS unset — usage.recorded emission disabled")
	}

	chatCompletions := handlers.NewChatCompletionsHandler(
		logger,
		handlers.WithAdapterRegistry(adapterRegistry),
		handlers.WithRouter(routingDecider),
		handlers.WithTokenDeducter(rateLimitMW),
		handlers.WithUsageEmitter(usageEmitter),
	)

	// Story 5.2 — key-policy enforcement gates (AC2 IP whitelist / AC3 model
	// scope / AC4 monthly cap). Runs AFTER bearer-auth (reads the extended
	// cache claims via middleware.CacheValueFromContext) and BEFORE ratelimit
	// + the chat/embeddings handlers so denied requests short-circuit at zero
	// upstream cost. Trusted-proxy CIDRs are sourced from the
	// `api-gateway-trusted-proxies` ConfigMap env vars (Q-E); empty → XFF
	// ignored, RemoteAddr wins (failsafe-against-misconfig).
	trustedProxies, badCIDRs := keypolicy.ParseTrustedProxies(
		os.Getenv("CLOUDFLARE_CIDRS"), os.Getenv("INGRESS_CIDRS"))
	for _, b := range badCIDRs {
		logger.Warn("apikey_trusted_proxy_cidr_parse_failed", slog.String("cidr", b))
	}
	logger.Info("apikey_trusted_proxies_loaded", slog.Int("count", len(trustedProxies)))
	keyPolicyRedis := redis.NewClient(mustRedisOptions(redisURL, logger))
	// Story 5.4 — fire-and-forget cap-threshold notifier (reuses the Story-2.6
	// notification-svc Connect client URL). The sticky-trip sentinel shares the
	// keypolicy Redis client.
	capNotifier := notifyclient.New(
		&http.Client{Timeout: 10 * time.Second},
		notificationSvcURL,
		logger,
	)
	keyPolicy := keypolicy.New(keypolicy.Options{
		Logger:         logger,
		TrustedProxies: trustedProxies,
		CostReader: func(ctx context.Context, apiKeyID string) (string, bool, error) {
			return usage.ReadMonthlyCostUSD(ctx, keyPolicyRedis, apiKeyID)
		},
		Metrics:     keypolicy.NewPolicyMetrics(),
		CapSentinel: keypolicy.NewRedisCapSentinel(keyPolicyRedis),
		Notifier:    capNotifier,
	})

	// Story 7.1 (Q-GATE) — pre-flight 402 balance gate. Reads the fast Redis
	// realtime mirror (balance:user:{id}:realtime); rejects when ≤ 0 BEFORE
	// upstream dispatch; FAIL-OPEN on a Redis outage (BR-A-4). Innermost wrap
	// around the chat handler so it runs just before dispatch with the resolved
	// user_id in context.
	billingGate := billinggate.New(billinggate.Options{
		Logger: logger,
		Reader: func(ctx context.Context, userID string) (string, bool, error) {
			return usage.ReadRealtimeBalance(ctx, keyPolicyRedis, userID)
		},
	})

	// Story 5.3 BR-X.4 / Architect Q9 — ratelimit runs AFTER bearer-auth
	// (needs the resolved api_key_id from context) and BEFORE the
	// chat-completions handler. Story 5.2 keypolicy sits between bearer-auth
	// and ratelimit. Story 7.1 billingGate is the innermost wrap (pre-dispatch).
	mux.Handle("POST /v1/chat/completions",
		bearerAuth.RequireAPIKey(keyPolicy(rateLimitMW.Wrap(billingGate(chatCompletions)))))

	// Story 7.1 (AC3) — read-only billing endpoints. Mounted behind bearer-auth
	// (user_id from the validated key). Wired only when a PG DSN is configured;
	// without it the routes are absent (404) rather than nil-panicking.
	if billingPool := buildBillingPool(logger); billingPool != nil {
		// Story 7.2 (Q-CONVLOC gateway-side) — boot+~60s-refresh FX rate snapshot
		// over the same read pool (mirrors billing-svc/internal/pricing). The
		// conversion read never calls the FX provider (BR-C-5); the provider is
		// touched only by the daily cron. A boot-load failure starts empty and the
		// refresh recovers — RMB requests degrade to USD until then (cold-start guard).
		var fxOpt []handlers.Option
		boot, ferr := fxrate.Load(ctx, billingPool)
		if ferr != nil {
			logger.Warn("fx-rate boot load failed — starting empty, refresh will recover",
				slog.String("error", ferr.Error()))
			boot = fxrate.NewSnapshot(nil)
		} else {
			logger.Info("fx-rate snapshot loaded", slog.Int("pairs", boot.Len()))
		}
		fxProvider := fxrate.NewProvider(boot, func(c context.Context) (*fxrate.Snapshot, error) {
			return fxrate.Load(c, billingPool)
		}, fxrate.DefaultRefreshInterval, logger)
		go fxProvider.Run(ctx)
		fxOpt = append(fxOpt, handlers.WithFxRateSource(fxProvider))

		billingRead := handlers.NewBillingReadHandler(logger, billingPool, fxOpt...)
		mux.Handle("GET /v1/balance", bearerAuth.RequireAPIKey(http.HandlerFunc(billingRead.Balance)))
		mux.Handle("GET /v1/usage", bearerAuth.RequireAPIKey(http.HandlerFunc(billingRead.Usage)))
		logger.Info("billing read endpoints wired (GET /v1/balance, GET /v1/usage; currency-aware)")
	} else {
		logger.Warn("HE_API_DB_POSTGRES_URI unset — GET /v1/balance + /v1/usage disabled")
	}

	// Story 3.5 — /v1/models (static catalogue) + /v1/embeddings (mock
	// vector). Per-route bearer-auth wrap mirrors Story 3.2 BR-1.4 +
	// Story 3.3 precedent. GET / POST method prefixes are load-bearing —
	// they make wrong-method requests fall through to a stdlib 405
	// without invoking the bearer-auth chain.
	modelsHandler := handlers.NewModelsHandler(logger)
	embeddingsHandler := handlers.NewEmbeddingsHandler(
		logger,
		handlers.WithEmbeddingTokenDeducter(rateLimitMW),
	)
	// /v1/models is a static catalogue — no token cost, NO ratelimit wrap.
	mux.Handle("GET /v1/models", bearerAuth.RequireAPIKey(modelsHandler))
	// /v1/embeddings consumes tokens — Story 5.3 BR-X.8 applies. Story 5.2
	// keypolicy enforces model-scope + IP-whitelist + cap here too (BR-3.2).
	mux.Handle("POST /v1/embeddings",
		bearerAuth.RequireAPIKey(keyPolicy(rateLimitMW.Wrap(embeddingsHandler))))

	// Story 4.7 — unauthenticated mirror of /v1/models. Mounted OUTSIDE
	// the bearer middleware chain; both handlers share a snapshot built
	// from the same modelsCatalogue + capabilitiesByModelID so the bodies
	// are byte-identical (4.7-INT-001 verifies). OQ-4.7-5 ratified the
	// constructor-injection sharing mechanism. The startedAt anchor is
	// pulled from the bearer-gated handler so the `created` value on the
	// public mirror matches the bearer endpoint within the same process.
	publicSnapshot := handlers.BuildPublicModelsSnapshot(modelsHandler.StartedAt())
	publicModelsHandler := handlers.NewPublicModelsHandler(logger, publicSnapshot)
	// Bare-path registration — OPTIONS preflight is handled by the
	// PublicCORS middleware wrapping the mux; method-not-allowed for
	// non-GET is emitted by the handler itself (canonical
	// 405_method_not_allowed envelope per OQ-4.7-6).
	mux.Handle("/public/models", publicModelsHandler)

	// Middleware chain (outer → inner): RequestID → SecurityHeaders → CSRF → mux.
	// Story 3.6 BR-2.7: requestid.RequestID is the OUTERMOST user-traffic wrap
	// so every response (success + error, including ones emitted by
	// SecurityHeaders / CSRF) carries X-He-Request-Id. The probeMux on
	// /health and /healthz bypasses this chain (Story 3.1 BR-1.3).
	// obs.WrapHTTPHandler stays the absolute outermost wrap so requestid
	// can read the OTel SpanContext that WrapHTTPHandler creates.
	csrfAllowed := csrfAllowlistFor(deployEnv)
	// Story 4.7 — cors.PublicCORS wraps the mux INNERMOST so the OQ-4.7-7
	// wildcard CORS policy is scoped strictly to `/public/*` paths and
	// the OPTIONS preflight short-circuits before reaching downstream
	// handlers. Non-`/public/*` requests flow through untouched.
	handler := requestid.RequestID(middleware.SecurityHeaders(middleware.CSRF(middleware.CSRFConfig{
		AllowedOrigins: csrfAllowed,
	}, cors.PublicCORS(mux))))

	// Story 3.1 — probeMux carries /health + /healthz on the bypass branch
	// (BR-1.3). It is dispatched by rootMux BEFORE the SecurityHeaders +
	// CSRF chain so probes are never rejected by Origin checks and never
	// pay the CSP / HSTS header bytes (BR-1.3). obs.WrapHTTPHandler at the
	// edge still wraps probeMux so /health spans land in Tempo (BR-1.4).
	//
	// TODO(post-3.2): extract to packages/go-observability/probemux.go once a
	// second consumer emerges (Wright Round 1 Q4 ruling).
	probeMux := http.NewServeMux()
	// Register the bare paths (method-agnostic) so HealthHandler dispatches
	// GET / HEAD / 405-other itself — preserves the JSON
	// {"error":"method_not_allowed"} body required by AC1 (Go 1.22 mux's
	// own 405 would be header-only).
	probeMux.HandleFunc("/health", health.Serve)
	probeMux.HandleFunc("/healthz", health.Serve)

	rootMux := http.NewServeMux()
	rootMux.Handle("/health", probeMux)
	rootMux.Handle("/healthz", probeMux)
	rootMux.Handle("/", handler)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           obs.WrapHTTPHandler(rootMux, serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info(
			"api-gateway listening",
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

// Story 5.3 rate-limit ceiling defaults + bounds. Source-of-truth lives
// in infra/helm/api-gateway/values.yaml `ratelimit.freeTierDefaults.*`
// (M-3 remediation); env vars templated by templates/deployment.yaml.
const (
	defaultRateLimitQPSMax = 10     // requests/second
	defaultRateLimitRPMMax = 300    // requests/minute
	defaultRateLimitTPMMax = 60_000 // tokens/minute

	// Defensive upper bounds — HALT-on-invalid if env value exceeds. Story
	// 5.3 Data Validation rows; 100M tokens/min is well above any plausible
	// commercial tier.
	maxRateLimitQPSMax = 100_000
	maxRateLimitRPMMax = 100_000
	maxRateLimitTPMMax = 100_000_000
)

// loadRateLimitCeilings reads RATELIMIT_FREE_TIER_{QPS,RPM,TPM}_MAX env
// vars, applies defaults for unset values, and HALT-validates the [1, max]
// range. Story 5.3 BR-X.7 — boot-time validation; gateway refuses to
// start on out-of-range values (operator-error protection).
func loadRateLimitCeilings() (ratelimit.Ceilings, error) {
	qpsMax, err := parseRateLimitEnv("RATELIMIT_FREE_TIER_QPS_MAX",
		defaultRateLimitQPSMax, maxRateLimitQPSMax)
	if err != nil {
		return ratelimit.Ceilings{}, err
	}
	rpmMax, err := parseRateLimitEnv("RATELIMIT_FREE_TIER_RPM_MAX",
		defaultRateLimitRPMMax, maxRateLimitRPMMax)
	if err != nil {
		return ratelimit.Ceilings{}, err
	}
	tpmMax, err := parseRateLimitEnv("RATELIMIT_FREE_TIER_TPM_MAX",
		defaultRateLimitTPMMax, maxRateLimitTPMMax)
	if err != nil {
		return ratelimit.Ceilings{}, err
	}
	return ratelimit.Ceilings{QPSMax: qpsMax, RPMMax: rpmMax, TPMMax: tpmMax}, nil
}

// parseRateLimitEnv reads `name` as a positive integer in [1, maxBound].
// Empty/unset → fallback. Returns a structured error on parse failure or
// out-of-range value so main() can log + HALT (Story 5.3 BR-X.7).
func parseRateLimitEnv(name string, fallback, maxBound int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("env %s=%q: %w", name, raw, err)
	}
	if v < 1 || v > maxBound {
		return 0, fmt.Errorf("env %s=%d out of range [1, %d]", name, v, maxBound)
	}
	return v, nil
}

// splitCSV splits a comma-separated env value into trimmed non-empty parts.
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildBillingPool builds the read pool for GET /v1/balance + /v1/usage from
// HE_API_DB_POSTGRES_URI. Returns nil (endpoints disabled) when unset / on a
// parse/connect error — a missing billing pool never blocks gateway boot.
func buildBillingPool(logger *slog.Logger) *pgxpool.Pool {
	uri := os.Getenv("HE_API_DB_POSTGRES_URI")
	if uri == "" {
		return nil
	}
	cfg, err := pgxpool.ParseConfig(uri)
	if err != nil {
		logger.Error("parse postgres uri — billing read endpoints disabled", slog.String("error", err.Error()))
		return nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		logger.Error("create postgres pool — billing read endpoints disabled", slog.String("error", err.Error()))
		return nil
	}
	return pool
}

// mustRedisOptions parses a Redis URL; on parse failure it logs WARN and
// returns the default loopback options (consistent with the Story-3.2
// lazy-init posture). Never panics — production wants degraded operation
// over a hard cold-start failure.
func mustRedisOptions(redisURL string, logger *slog.Logger) *redis.Options {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		logger.Warn(
			"ratelimit redis URL parse failed — using loopback fallback",
			slog.String("url", redisURL),
			slog.String("error", err.Error()),
		)
		return &redis.Options{Addr: "127.0.0.1:6379"}
	}
	return opt
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
