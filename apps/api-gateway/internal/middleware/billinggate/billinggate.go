// Package billinggate is the Story 7.1 (AC3 / T3.3) pre-flight balance gate on
// the chat hot path. BEFORE upstream dispatch it reads the fast Redis realtime
// mirror (balance:user:{id}:realtime — NO billing-svc round-trip) and rejects
// with 402_balance_insufficient when the balance is ≤ 0 (hard-zero threshold,
// BR-A-6). It is FAIL-OPEN (Architect Q-GATE): a Redis outage / parse error /
// absent key ALLOWS the request — never block paying traffic on a cache outage
// (parity with the 5.x ratelimit/keypolicy fail-open posture, BR-A-4).
package billinggate

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// BalanceReader returns the realtime balance string for a user, whether it was
// found, and any error. A nil reader (or a not-found / error result) fails OPEN.
type BalanceReader func(ctx context.Context, userID string) (balance string, found bool, err error)

// Options configures the gate.
type Options struct {
	Reader BalanceReader
	Logger *slog.Logger
}

// New builds the pre-flight gate middleware. With a nil reader the gate is a
// transparent pass-through (billing disabled).
func New(opts Options) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	meter := otel.Meter("apps/api-gateway/internal/middleware/billinggate")
	rejects, _ := meter.Int64Counter("he_billing_gate_rejections_total",
		metric.WithDescription("Pre-flight 402 balance_insufficient rejections"))
	failOpen, _ := meter.Int64Counter("he_billing_gate_fail_open_total",
		metric.WithDescription("Pre-flight gate fail-open events (Redis unavailable/parse error)"))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if opts.Reader == nil {
				next.ServeHTTP(w, r)
				return
			}
			userID, ok := middleware.BearerUserIDFromContext(ctx)
			if !ok || userID == "" {
				// No bound user — cannot gate; allow (defence-in-depth, never block).
				next.ServeHTTP(w, r)
				return
			}

			raw, found, err := opts.Reader(ctx, userID)
			if err != nil {
				failOpenInc(ctx, failOpen)
				logger.WarnContext(ctx, "billing_gate_fail_open",
					slog.String("event", "billing_gate_fail_open"),
					slog.String("reason", "reader_error"),
					slog.String("error", err.Error()),
				)
				next.ServeHTTP(w, r)
				return
			}
			if !found {
				// No realtime mirror yet (new user / not-yet-deducted) — allow.
				next.ServeHTTP(w, r)
				return
			}
			bal, perr := decimal.NewFromString(raw)
			if perr != nil {
				failOpenInc(ctx, failOpen)
				logger.WarnContext(ctx, "billing_gate_fail_open",
					slog.String("event", "billing_gate_fail_open"),
					slog.String("reason", "parse_error"),
				)
				next.ServeHTTP(w, r)
				return
			}

			if bal.LessThanOrEqual(decimal.Zero) {
				if rejects != nil {
					rejects.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "balance_insufficient")))
				}
				_ = openaierr.Write(w, ctx, http.StatusPaymentRequired,
					"402_balance_insufficient",
					"Insufficient balance. Please top up to continue.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func failOpenInc(ctx context.Context, c metric.Int64Counter) {
	if c != nil {
		c.Add(ctx, 1)
	}
}
