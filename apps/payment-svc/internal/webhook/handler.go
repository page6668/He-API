// Package webhook is payment-svc's inbound webhook ingress (Story 7.3, AC3). The
// gateway transparently reverse-proxies the RAW provider webhook bytes here
// (Q-WEBHOOK-INGRESS) — this handler is where the signature is verified against
// the exact bytes and, on success, a `payment.completed` event is produced.
//
// The provider SIGNATURE is the sole credential (the route is unauthenticated —
// the gateway mounts the public route OUTSIDE the bearer chain, BR-W-1). The
// status-code discipline (BR-W-5):
//   - bad / absent / stale / tampered signature → 400 (body NEVER parsed into a
//     money action);
//   - verified but no-op event (refund/dispute/unknown) → 200 ACK (Q-REFUND);
//   - verified + produced ok → 200;
//   - verified but the produce FAILS (transient) → 5xx so the provider redelivers
//     (at-least-once + idempotent apply downstream).
package webhook

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// maxBody bounds the webhook body read (defence against a giant POST).
const maxBody = 1 << 20 // 1 MiB

// Emitter publishes a verified PaymentEvent (satisfied by *producer.Producer).
type Emitter interface {
	Emit(ctx context.Context, ev *paymentv1.PaymentEvent) error
}

// Handler verifies + forwards provider webhooks.
type Handler struct {
	providers *provider.Registry
	emitter   Emitter
	logger    *slog.Logger
	now       func() time.Time

	total metric.Int64Counter // he_payment_webhook_total{provider,result}
}

// New constructs a Handler. emitter may be nil (verified events are then logged
// + counted but not produced — useful before Kafka is wired).
func New(providers *provider.Registry, emitter Emitter, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{providers: providers, emitter: emitter, logger: logger, now: time.Now}
	h.total, _ = otel.Meter("apps/payment-svc/internal/webhook").Int64Counter(
		"he_payment_webhook_total",
		metric.WithDescription("Inbound provider webhooks by provider + result (processed/rejected_signature/unhandled/parse_error/emit_failed)"),
	)
	return h
}

// WithClock overrides the clock (tests).
func (h *Handler) WithClock(now func() time.Time) *Handler { h.now = now; return h }

// Handle returns an http.HandlerFunc bound to a specific provider id. The server
// mounts one per provider (/webhooks/stripe, /webhooks/paypal).
func (h *Handler) Handle(providerName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		prov, err := h.providers.Get(providerName)
		if err != nil {
			// Unknown provider id — not a signature failure; 404 (route shouldn't exist).
			http.NotFound(w, r)
			return
		}

		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			h.count(ctx, providerName, "read_error")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		ev, verr := prov.VerifyWebhook(ctx, rawBody, r.Header)
		if verr != nil {
			// Signature invalid OR a verified-but-unparseable body — either way we
			// will NOT credit. Forgery/replay reject with 400; the body is dropped.
			reason := "parse_error"
			if errors.Is(verr, provider.ErrSignatureInvalid) {
				reason = "rejected_signature"
			}
			h.count(ctx, providerName, reason)
			h.logger.WarnContext(ctx, "payment_webhook_rejected",
				slog.String("event", "payment_webhook_rejected"),
				slog.String("provider", providerName),
				slog.String("reason", reason),
			)
			// 400 — body never trusted into a money action (BR-W-1).
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Verified. A no-op event (refund/dispute/unknown) is ACK'd 200 so the
		// provider stops retrying, but emits nothing (Q-REFUND / BR-W-5).
		if ev.Kind == provider.EventUnhandled {
			h.count(ctx, providerName, "unhandled")
			h.logger.InfoContext(ctx, "payment_webhook_unhandled",
				slog.String("event", "payment_webhook_unhandled"),
				slog.String("provider", providerName),
			)
			w.WriteHeader(http.StatusOK)
			return
		}

		out := &paymentv1.PaymentEvent{
			OrderId:                ev.OrderID,
			PaymentProvider:        ev.Provider,
			ExternalOrderId:        ev.ExternalOrderID,
			SettledAmount:          ev.SettledAmount,
			Currency:               ev.Currency,
			Status:                 ev.Status,
			EventType:              string(ev.Kind),
			ExternalSubscriptionId: ev.ExternalSubscriptionID,
			Plan:                   ev.Plan, // Story 7.8 — carries the confirmed tier (BR-S-3)
			Ts:                     h.now().UTC().Format(time.RFC3339),
		}

		if h.emitter != nil {
			if err := h.emitter.Emit(ctx, out); err != nil {
				// Transient produce failure → 5xx so the provider redelivers
				// (at-least-once + idempotent downstream, BR-W-5).
				h.count(ctx, providerName, "emit_failed")
				h.logger.ErrorContext(ctx, "payment_webhook_emit_failed",
					slog.String("event", "payment_webhook_emit_failed"),
					slog.String("provider", providerName),
					slog.String("error", err.Error()),
				)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
		}

		h.count(ctx, providerName, "processed")
		// Non-PII: order_id + provider + kind only (NO card data — PCI §8.4; NO secrets).
		h.logger.InfoContext(ctx, "payment_webhook_processed",
			slog.String("event", "payment_webhook_processed"),
			slog.String("provider", providerName),
			slog.String("kind", string(ev.Kind)),
			slog.String("order_id", ev.OrderID),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func (h *Handler) count(ctx context.Context, providerName, result string) {
	if h == nil || h.total == nil {
		return
	}
	h.total.Add(ctx, 1, metric.WithAttributes(
		attribute.String("provider", providerName),
		attribute.String("result", result),
	))
}
