// auth-svc — He-API authentication service (Story 2.2).
//
// Internal-only Connect/gRPC service (Wright Round 1 Q1 ruling). Exposes the
// five AuthService RPCs. P2f wires RegisterUser end-to-end; VerifyEmail /
// ResendVerification (P3) and LoginUser / RefreshToken (P4) remain
// CodeUnimplemented stubs with phase pointers in the handler.
//
// Env config (all from K8s Secrets / ConfigMap mounted by infra/helm/auth-svc):
//
//	HE_API_DB_POSTGRES_URI       — Story 1.6 K8s Secret he-api-db-creds
//	HE_API_DB_REDIS_URI          — Story 1.6 K8s Secret he-api-db-creds
//	HE_API_NOTIFICATION_SVC_URL  — notification-svc ClusterIP base URL
//	                               (e.g. http://notification-svc:8080)
//	HE_API_CONSOLE_BASE_URL      — public console URL used to build the
//	                               verification email link
//	                               (e.g. https://console.he-api.com)
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
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/auth-svc/internal/apikey"
	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	authjwt "github.com/he-api/he-api/apps/auth-svc/internal/jwt"
	"github.com/he-api/he-api/apps/auth-svc/internal/kms"
	"github.com/he-api/he-api/apps/auth-svc/internal/metrics"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/password"
	"github.com/he-api/he-api/apps/auth-svc/internal/redisclient"

	"github.com/google/uuid"
)

// mfaIssuerAdapter bridges *authjwt.Signer.IssueMFAToken (which takes
// authjwt.MFATokenInput) to handlers.MFATokenIssuer (which takes
// handlers.MFAIssueInput). The two struct shapes are isomorphic but the
// interface keeps the handlers package free of the jwt import.
type mfaIssuerAdapter struct {
	signer *authjwt.Signer
}

func (a mfaIssuerAdapter) IssueMFAToken(in handlers.MFAIssueInput, now time.Time) (string, string, error) {
	return a.signer.IssueMFAToken(authjwt.MFATokenInput{
		UserID:        in.UserID,
		LoginMethod:   in.LoginMethod,
		IPHash:        in.IPHash,
		UserAgentHash: in.UserAgentHash,
		ReturnTo:      in.ReturnTo,
	}, now)
}

// mfaParserAdapter bridges *authjwt.Verifier.ParseMFAToken to
// handlers.MFATokenParser.
type mfaParserAdapter struct {
	verifier *authjwt.Verifier
}

func (a mfaParserAdapter) ParseMFAToken(tok string) (*handlers.MFAParsedClaims, error) {
	c, err := a.verifier.ParseMFAToken(tok)
	if err != nil {
		return nil, err
	}
	return &handlers.MFAParsedClaims{
		Subject:       c.Subject,
		JTI:           c.JTI,
		Audience:      c.Audience,
		Purpose:       c.Purpose,
		LoginMethod:   c.LoginMethod,
		IPHash:        c.IPHash,
		UserAgentHash: c.UserAgentHash,
		ReturnTo:      c.ReturnTo,
		IssuedAt:      c.IssuedAt,
		ExpiresAt:     c.ExpiresAt,
	}, nil
}

// Silence unused-import linter (the uuid import is consumed by adapter types
// in their concrete handler call sites — the cmd/server file itself doesn't
// reference uuid directly).
var _ = uuid.Nil

const (
	serviceName    = "auth-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
	listenAddr     = ":8080"

	defaultNotificationSvcURL = "http://notification-svc:8080"
	defaultConsoleBaseURL     = "http://localhost:3000"

	// auditTopicName is fixed per TS-CONS-015 (sharing the existing
	// `audit.event` topic with 30-day retention; no new topic this Story).
	auditTopicName = "audit.event"

	// auditKafkaWriteTimeout caps how long a single WriteMessages
	// queue-attempt can block. Under Async=true this is the enqueue
	// timeout, not the broker round-trip — the latter is bounded by
	// the writer's BatchTimeout / WriteTimeout below.
	auditKafkaWriteTimeout = 5 * time.Second
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

	// === OTel meter provider (Story 2.2 T4.7 — Prometheus exporter) =========
	// Installs a MeterProvider whose Prometheus exporter registers
	// on prometheus.DefaultRegisterer; obs.WrapHTTPHandler already
	// serves /metrics via promhttp.Handler() so OTel counters surface
	// in the scrape automatically. MUST be installed BEFORE metrics.New()
	// — instruments registered against the no-op provider don't switch.
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

	// === audit publisher (Kafka if HE_API_AUDIT_KAFKA_BROKERS set; NoOp else) ===
	// Production runs against a managed Kafka broker per Story 1.6
	// data-models.md §4.4 (topic `audit.event`, 30-day retention,
	// TS-CONS-015). Dev / unit-test environments leave the env var
	// unset and the NoOpPublisher path keeps the audit shape observable
	// via structured logs without a broker dependency.
	var (
		auditPub    audit.Publisher
		kafkaWriter *kafka.Writer
	)
	if rawBrokers := strings.TrimSpace(os.Getenv("HE_API_AUDIT_KAFKA_BROKERS")); rawBrokers != "" {
		brokers := splitAndTrim(rawBrokers, ",")
		kafkaWriter = &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Topic:                  auditTopicName,
			Balancer:               &kafka.Hash{},    // hash(EmailHash) — per-account partition stability (BR-4.5)
			RequiredAcks:           kafka.RequireOne, // TS-CONS-009: acks=1
			Async:                  true,             // TS-CONS-009: non-blocking — errors surface via Completion
			AllowAutoTopicCreation: false,            // topic is pre-provisioned by infra
			WriteTimeout:           auditKafkaWriteTimeout,
			Completion: func(messages []kafka.Message, err error) {
				if err != nil {
					// Per UNIT-181: broker outage MUST NOT block business —
					// warn-log only. The handler call path used
					// PublishBestEffort which already returned nil to the
					// caller; this is the async-publish failure backchannel.
					logger.Warn(
						"audit kafka publish failed (async)",
						slog.String("topic", auditTopicName),
						slog.Int("msg_count", len(messages)),
						slog.String("error", err.Error()),
					)
				}
			},
		}
		auditPub = audit.NewKafkaPublisher(kafkaWriter, logger)
		logger.Info(
			"audit publisher: kafka",
			slog.String("topic", auditTopicName),
			slog.Int("broker_count", len(brokers)),
		)
		defer func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer ccancel()
			// Close drains the async queue before returning.
			if err := kafkaWriter.Close(); err != nil {
				logger.Warn("audit kafka close failed", slog.String("error", err.Error()))
			}
			_ = cctx
		}()
	} else {
		logger.Warn("HE_API_AUDIT_KAFKA_BROKERS unset — audit using NoOpPublisher (dev mode)")
		auditPub = audit.NewNoOpPublisher(logger)
	}

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
		logger.Error(
			"JWT key mismatch — Signer kid ≠ Verifier kid (keypair drift)",
			slog.String("signer_kid", jwtSigner.KeyID()),
			slog.String("verifier_kid", jwtVerifier.KeyID()),
		)
		os.Exit(1)
	}

	// === Metrics (BR-4.8) ==================================================
	// Registers the 5 auth-svc counters on the global OTel meter provider.
	// In production no Prometheus exporter is wired yet — the counters
	// register on the default no-op meter and Add() calls are zero-cost.
	// The Story 2.2 T4.7 follow-up wires the obs.NewMeterProvider +
	// /metrics endpoint; instrumentation call sites are stable now so the
	// later wiring is a single-PR drop-in.
	metricsCounters, err := metrics.New()
	if err != nil {
		logger.Error("register OTel counters", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// === Story 2.4 — KMS master key + MFA signing/parsing adapters ==========
	// Master key file format: 32 raw bytes (AES-256). Generation/rotation is
	// an ops runbook (Wright Round 1 Q1 ruling — K8s Secret bootstrap path).
	kmsKeyPath := envOr("HE_API_KMS_MASTER_KEY_PATH", "/etc/auth-svc/keys/kms_master.bin")
	kmsKeyBytes, err := os.ReadFile(kmsKeyPath)
	if err != nil {
		logger.Error("read KMS master key", slog.String("path", kmsKeyPath), slog.String("error", err.Error()))
		os.Exit(1)
	}
	kmsClient, err := kms.NewLocal(kmsKeyBytes)
	if err != nil {
		logger.Error("init KMS client", slog.String("error", err.Error()))
		os.Exit(1)
	}
	mfaIssuer := mfaIssuerAdapter{signer: jwtSigner}
	mfaParser := mfaParserAdapter{verifier: jwtVerifier}

	// === Story 3.2 — API-key validator (AC2) + Story 5.1 management ========
	// Story 3.2 backs AuthService.ValidateApiKey (Validate hot path).
	// Story 5.1 extends with CreateApiKey + ListApiKeys + RevokeApiKey.
	// The same pgxpool serves both paths; the Redis client (already
	// constructed for ratelimit) doubles as the sentinel writer for the
	// BR-3.8 cross-pod cache-invalidation contract.
	apiKeyService := apikey.NewServiceWithMgmt(
		apikey.QuerierRepository{Q: pgPool},
		tp.Tracer("apps/auth-svc/internal/apikey"),
		logger,
		auditPub,
		redisclient.NewRevokeSentinel(rdb),
	)

	// === AuthServer =========================================================
	authServer := handlers.NewAuthServer(handlers.AuthServer{
		DB:             pgPool,
		DBTx:           pgPool,
		Redis:          rdb,
		HIBP:           hibp,
		Notification:   notifClient,
		Audit:          auditPub,
		JWT:            jwtSigner,
		JWTVerify:      jwtVerifier,
		Metrics:        metricsCounters,
		Clock:          time.Now,
		ConsoleBaseURL: envOr("HE_API_CONSOLE_BASE_URL", defaultConsoleBaseURL),
		Logger:         logger,
		// Story 2.4 deps
		KMS:       kmsClient,
		MFASigner: mfaIssuer,
		MFAParser: mfaParser,
		Issuer:    envOr("HE_API_TOTP_ISSUER", "He-API"),
		// Story 3.2 dep — API-key bearer-auth validator (AC2).
		APIKey: apiKeyService,
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
		logger.Info(
			"auth-svc listening",
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

// splitAndTrim splits s by sep and trims whitespace from each element,
// dropping any empty entries. Used to parse comma-separated broker lists
// from env vars where stray whitespace around commas is common.
func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
