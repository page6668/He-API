// Package server assembles payment-svc's HTTP surface: the PaymentService
// Connect-RPC handler (CreateCheckout / CreateSubscription) + the inbound webhook
// routes (/webhooks/stripe, /webhooks/paypal, /webhooks/coinbase, /webhooks/alipay — the gateway
// reverse-proxies the raw bytes here, Q-WEBHOOK-INGRESS) + /livez + /ready, wrapped with the shared
// observability middleware (which serves /metrics). Mirrors apps/billing-svc/
// internal/server.
package server

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	obs "github.com/he-api/he-api/packages/go-observability"

	"github.com/he-api/he-api/apps/payment-svc/internal/webhook"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

// Options configures a Server.
type Options struct {
	// PaymentHandler implements the PaymentService Connect handler. May be nil
	// (falls back to the Unimplemented stub — health still serves).
	PaymentHandler paymentv1connect.PaymentServiceHandler
	// WebhookHandler verifies + forwards inbound provider webhooks. May be nil
	// (the webhook routes are then not mounted).
	WebhookHandler *webhook.Handler
	// Logger may be nil (slog.Default()).
	Logger *slog.Logger
	// ServiceName labels the observability middleware (default "payment-svc").
	ServiceName string
}

// Server holds the assembled HTTP handler and the readiness gate.
type Server struct {
	Handler http.Handler
	ready   atomic.Bool
}

// New wires the HTTP handler. Readiness starts FALSE: /ready returns 503 until
// SetReady(true) once the listener is up.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	name := opts.ServiceName
	if name == "" {
		name = "payment-svc"
	}
	handler := opts.PaymentHandler
	if handler == nil {
		handler = paymentv1connect.UnimplementedPaymentServiceHandler{}
	}

	s := &Server{}

	mux := http.NewServeMux()
	path, h := paymentv1connect.NewPaymentServiceHandler(handler)
	mux.Handle(path, h)

	if opts.WebhookHandler != nil {
		// Internal webhook ingress. The gateway forwards the RAW request body here
		// byte-for-byte (raw-body integrity for HMAC, BR-W-2). One route per
		// provider so the impl is selected by path, not by parsing the body.
		mux.HandleFunc("POST /webhooks/stripe", opts.WebhookHandler.Handle("stripe"))
		mux.HandleFunc("POST /webhooks/paypal", opts.WebhookHandler.Handle("paypal"))
		mux.HandleFunc("POST /webhooks/coinbase", opts.WebhookHandler.Handle("coinbase")) // Story 7.4 (USDC)
		mux.HandleFunc("POST /webhooks/alipay", opts.WebhookHandler.Handle("alipay"))     // Story 7.5 (Alipay+)
	}

	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})

	s.Handler = obs.WrapHTTPHandler(mux, name)
	return s
}

// SetReady flips the readiness gate.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Ready reports the current readiness state.
func (s *Server) Ready() bool { return s.ready.Load() }
