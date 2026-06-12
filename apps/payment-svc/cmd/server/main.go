// payment-svc — He-API payment-channel integration service (Epic 7, Story 7.3).
// FIRST realisation of the payment-svc named in service-topology §3.2.
//
// Responsibilities (Q-SVC):
//   - the pluggable PaymentProvider seam (Stripe + PayPal in 7.3; 7.4-7.6 extend);
//   - inbound webhook signature verification (the gateway reverse-proxies the raw
//     bytes to /webhooks/{stripe,paypal} — Q-WEBHOOK-INGRESS);
//   - the `payment.completed` Kafka PRODUCER (consumed by billing-svc credit +
//     notification-svc receipt).
//
// payment-svc does NOT touch the money ledger — it produces payment.completed and
// billing-svc applies the exactly-once credit (Q-ORDEROWNER, blast-radius
// isolation of the provider SDKs + secrets from the ledger core).
//
// Observability (TracerProvider + Prometheus meter + slog JSON + /metrics) is
// REUSED from packages/go-observability (Story 1.5). Provider secrets are
// env-injected + slog-redacted, NEVER logged (Q-SECRETS).
//
// Env config:
//
//	PORT                          — listen port (default 8080)
//	HE_API_KAFKA_BROKERS          — comma-separated Kafka brokers (payment.completed producer)
//	STRIPE_SECRET_KEY             — Stripe REST secret key (sk_...)
//	STRIPE_WEBHOOK_SIGNING_SECRET — Stripe webhook HMAC secret (whsec_...; distinct blast radius)
//	STRIPE_API_BASE_URL           — optional Stripe API base override (sandbox/test)
//	PAYPAL_CLIENT_ID              — PayPal REST client id
//	PAYPAL_CLIENT_SECRET          — PayPal REST client secret
//	PAYPAL_WEBHOOK_ID             — PayPal webhook id (verify-webhook-signature binds to it)
//	PAYPAL_API_BASE_URL           — optional PayPal API base override (sandbox/test)
//	COINBASE_COMMERCE_API_KEY     — Coinbase Commerce REST API key (X-CC-Api-Key; charge create)
//	COINBASE_COMMERCE_WEBHOOK_SECRET — Coinbase webhook HMAC secret (X-CC-Webhook-Signature; distinct blast radius)
//	COINBASE_COMMERCE_API_BASE_URL — optional Coinbase Commerce API base override (sandbox/test)
//	ALIPAY_PLUS_CLIENT_ID         — Alipay+/Antom Client-Id (gates the channel)
//	ALIPAY_PLUS_MERCHANT_PRIVATE_KEY — RSA key signing OUR outbound Antom calls (⚠️ broad blast radius)
//	ALIPAY_PLUS_ALIPAY_PUBLIC_KEY — Alipay+'s public key verifying inbound notifications
//	ALIPAY_PLUS_API_BASE_URL / ALIPAY_PLUS_NOTIFY_PATH — optional base / public-notify-path overrides
//	WECHAT_PAY_MCH_ID             — WeChat Pay merchant id (gates the channel)
//	WECHAT_PAY_MERCHANT_PRIVATE_KEY — RSA key signing OUR outbound WeChat calls (⚠️ broad blast radius; NEVER logged)
//	WECHAT_PAY_MERCHANT_CERT_SERIAL — our merchant cert serial (outbound Authorization serial_no)
//	WECHAT_PAY_PLATFORM_PUBLIC_KEY  — WeChat platform public key verifying inbound callbacks (not a secret)
//	WECHAT_PAY_PLATFORM_CERT_SERIAL — platform cert serial matched against Wechatpay-Serial
//	WECHAT_PAY_APIV3_KEY          — ⚠️ 32B symmetric AES-256 key; decrypts EVERY callback resource (NEVER logged)
//	WECHAT_PAY_APPID / WECHAT_PAY_API_BASE_URL / WECHAT_PAY_NOTIFY_URL — appid / base (HK vs global) / public callback URL
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

	"go.opentelemetry.io/otel"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/payment-svc/internal/paymentgrpc"
	"github.com/he-api/he-api/apps/payment-svc/internal/producer"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/alipay"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/coinbase"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/paypal"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/stripe"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/wechat"
	"github.com/he-api/he-api/apps/payment-svc/internal/server"
	"github.com/he-api/he-api/apps/payment-svc/internal/webhook"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

const (
	serviceName    = "payment-svc"
	serviceNS      = "he-api-staging"
	serviceVersion = "0.0.1"
)

func main() {
	logger := obs.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, ":"+envOr("PORT", "8080")); err != nil {
		logger.Error("payment-svc boot failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, addr string) error {
	tp, err := obs.NewTracerProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetTracerProvider(tp)
	obs.SetupPropagation() // Story 9.4 BR-TR-1 — global W3C propagator (extract incoming traceparent)
	defer shutdown(tp.Shutdown)

	mp, err := obs.NewMeterProvider(ctx, serviceName, serviceNS, serviceVersion)
	if err != nil {
		return err
	}
	otel.SetMeterProvider(mp)
	defer shutdown(mp.Shutdown)

	// Provider seam — wired from env. Each provider is added only when its secrets
	// are present, so a partial config (e.g. Stripe only in dev) still boots.
	registry := buildRegistry(logger)

	// payment.completed Kafka producer (best-effort wiring — verified webhooks log
	// + count even without Kafka, but cannot drive credit until it is configured).
	var emitter webhook.Emitter
	var closeWriter = func() {}
	if brokersEnv := os.Getenv("HE_API_KAFKA_BROKERS"); brokersEnv != "" {
		w := producer.NewKafkaWriter(strings.Split(brokersEnv, ","))
		emitter = producer.New(w)
		closeWriter = func() { _ = w.Close() }
		logger.Info("payment.completed producer wired", slog.String("topic", producer.Topic))
	} else {
		logger.Warn("HE_API_KAFKA_BROKERS unset — payment.completed producer disabled (webhooks verify but do not drive credit)")
	}
	defer closeWriter()

	paymentHandler := unimplementedOrReal(registry, logger)
	webhookHandler := webhook.New(registry, emitter, logger)

	srv := server.New(server.Options{
		PaymentHandler: paymentHandler,
		WebhookHandler: webhookHandler,
		Logger:         logger,
		ServiceName:    serviceName,
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("payment-svc listening", slog.String("addr", addr),
			slog.String("providers", strings.Join(registry.Names(), ",")))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	srv.SetReady(true)

	select {
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
	case err := <-serverErr:
		return err
	}

	srv.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// buildRegistry wires the PaymentProvider impls whose secrets are configured.
func buildRegistry(logger *slog.Logger) *provider.Registry {
	var impls []provider.PaymentProvider

	if sk := os.Getenv("STRIPE_SECRET_KEY"); sk != "" {
		var opts []stripe.Option
		if base := os.Getenv("STRIPE_API_BASE_URL"); base != "" {
			opts = append(opts, stripe.WithBaseURL(base))
		}
		impls = append(impls, stripe.New(sk, os.Getenv("STRIPE_WEBHOOK_SIGNING_SECRET"), opts...))
		logger.Info("stripe provider wired")
	} else {
		logger.Warn("STRIPE_SECRET_KEY unset — stripe provider disabled")
	}

	if cid := os.Getenv("PAYPAL_CLIENT_ID"); cid != "" {
		var opts []paypal.Option
		if base := os.Getenv("PAYPAL_API_BASE_URL"); base != "" {
			opts = append(opts, paypal.WithBaseURL(base))
		}
		impls = append(impls, paypal.New(cid, os.Getenv("PAYPAL_CLIENT_SECRET"), os.Getenv("PAYPAL_WEBHOOK_ID"), opts...))
		logger.Info("paypal provider wired")
	} else {
		logger.Warn("PAYPAL_CLIENT_ID unset — paypal provider disabled")
	}

	// Story 7.4 — USDC via Coinbase Commerce. Added only when the API key is set, so
	// a partial config (e.g. no crypto channel in dev) still boots. The webhook
	// secret is distinct from the API key (distinct blast radius, BR-W-6).
	if apiKey := os.Getenv("COINBASE_COMMERCE_API_KEY"); apiKey != "" {
		var opts []coinbase.Option
		if base := os.Getenv("COINBASE_COMMERCE_API_BASE_URL"); base != "" {
			opts = append(opts, coinbase.WithBaseURL(base))
		}
		impls = append(impls, coinbase.New(apiKey, os.Getenv("COINBASE_COMMERCE_WEBHOOK_SECRET"), opts...))
		logger.Info("coinbase provider wired")
	} else {
		logger.Warn("COINBASE_COMMERCE_API_KEY unset — coinbase (USDC) provider disabled")
	}

	// Story 7.5 — Alipay+ / Antom international cashier. Added only when the client
	// id is set, so a partial config (e.g. no Alipay+ channel in dev) still boots.
	// The merchant PRIVATE key (signs outbound) is distinct from the Alipay+ PUBLIC
	// key (verifies inbound) and the client id (⚠️ the private key has a broader
	// blast radius than an HMAC secret, BR-W-6). WithNotifyPath carries the PUBLIC
	// notify path Antom signs over (Q-NOTIFY-PATH), not the internal proxied path.
	if cid := os.Getenv("ALIPAY_PLUS_CLIENT_ID"); cid != "" {
		opts := []alipay.Option{}
		if base := os.Getenv("ALIPAY_PLUS_API_BASE_URL"); base != "" {
			opts = append(opts, alipay.WithBaseURL(base))
		}
		if np := os.Getenv("ALIPAY_PLUS_NOTIFY_PATH"); np != "" {
			opts = append(opts, alipay.WithNotifyPath(np))
		}
		impls = append(impls, alipay.New(cid,
			os.Getenv("ALIPAY_PLUS_MERCHANT_PRIVATE_KEY"),
			os.Getenv("ALIPAY_PLUS_ALIPAY_PUBLIC_KEY"), opts...))
		logger.Info("alipay provider wired")
	} else {
		logger.Warn("ALIPAY_PLUS_CLIENT_ID unset — alipay (Alipay+) provider disabled")
	}

	// Story 7.6 — WeChat Pay HK / cross-border (the 5th/FINAL channel). Added only
	// when the merchant id is set, so a partial config (e.g. no WeChat channel in
	// dev) still boots. TWO high-value secrets: the merchant PRIVATE key signs OUR
	// outbound calls (⚠️ broad blast radius) and the symmetric APIv3Key decrypts
	// EVERY callback resource (⚠️ a NEW decrypt secret no prior channel held); both
	// are distinct from the platform PUBLIC verify key (a non-secret) and the
	// mch/cert serials (BR-W-7). The platform serial selects the verify key
	// (Wechatpay-Serial rotation, Q-CERT-ROTATION).
	if mch := os.Getenv("WECHAT_PAY_MCH_ID"); mch != "" {
		opts := []wechat.Option{}
		if base := os.Getenv("WECHAT_PAY_API_BASE_URL"); base != "" {
			opts = append(opts, wechat.WithBaseURL(base))
		}
		if nu := os.Getenv("WECHAT_PAY_NOTIFY_URL"); nu != "" {
			opts = append(opts, wechat.WithNotifyURL(nu))
		}
		if appID := os.Getenv("WECHAT_PAY_APPID"); appID != "" {
			opts = append(opts, wechat.WithAppID(appID))
		}
		impls = append(impls, wechat.New(mch,
			os.Getenv("WECHAT_PAY_MERCHANT_PRIVATE_KEY"),
			os.Getenv("WECHAT_PAY_MERCHANT_CERT_SERIAL"),
			os.Getenv("WECHAT_PAY_PLATFORM_PUBLIC_KEY"),
			os.Getenv("WECHAT_PAY_PLATFORM_CERT_SERIAL"),
			os.Getenv("WECHAT_PAY_APIV3_KEY"), opts...))
		logger.Info("wechat provider wired")
	} else {
		logger.Warn("WECHAT_PAY_MCH_ID unset — wechat (WeChat Pay HK) provider disabled")
	}

	return provider.NewRegistry(impls...)
}

// unimplementedOrReal returns the real PaymentService handler when at least one
// provider is wired, else interface-nil (server.New falls back to Unimplemented).
func unimplementedOrReal(registry *provider.Registry, logger *slog.Logger) paymentv1connect.PaymentServiceHandler {
	if len(registry.Names()) == 0 {
		return nil
	}
	return paymentgrpc.NewServer(registry, logger)
}

func shutdown(fn func(context.Context) error) {
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = fn(sctx)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
