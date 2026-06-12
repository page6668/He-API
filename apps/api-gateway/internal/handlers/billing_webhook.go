// Story 7.3 (AC3) — the inbound webhook ingress at the gateway. Per the Architect
// Q-WEBHOOK-INGRESS ruling, the gateway hosts the public webhook route as a
// TRANSPARENT reverse proxy: it reads the raw request body bytes ONCE and forwards
// them VERBATIM (no json.Unmarshal / re-marshal / normalization) to payment-svc's
// own HTTP webhook handler, which verifies the provider signature against the
// EXACT bytes. Raw-body integrity (BR-W-2) is therefore guaranteed at this layer
// because the gateway never deserializes the body.
//
// These routes are mounted OUTSIDE the bearer chain AND outside the CSRF chain
// (the provider signature is the sole credential — a provider POST carries no
// Origin/cookie, so CSRF must not apply; mirrors the probeMux bypass). The signing
// secrets live only in payment-svc — the gateway is a byte pipe with no provider
// knowledge.
package handlers

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	obs "github.com/he-api/he-api/packages/go-observability"
)

// maxWebhookBody bounds the proxied body (defence against a giant POST).
const maxWebhookBody = 1 << 20 // 1 MiB

// WebhookProxyHandler byte-faithfully forwards provider webhooks to payment-svc.
type WebhookProxyHandler struct {
	baseURL string // payment-svc base URL (e.g. http://payment-svc:8080)
	client  *http.Client
	logger  *slog.Logger
}

// NewWebhookProxyHandler builds the proxy. An empty baseURL makes every webhook
// return 503 (payment-svc not configured) rather than nil-panicking.
func NewWebhookProxyHandler(logger *slog.Logger, baseURL string, client *http.Client) *WebhookProxyHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if client == nil {
		client = obs.NewHTTPClient(obs.WithTimeout(15 * time.Second)) // Story 9.4 BR-TR-2
	}
	return &WebhookProxyHandler{baseURL: baseURL, client: client, logger: logger}
}

// Handle returns an http.HandlerFunc bound to a provider id. It forwards the raw
// body + the provider signature headers to payment-svc /webhooks/{provider} and
// relays the status code back to the provider (so the provider's 2xx/4xx/5xx
// retry semantics are preserved end-to-end — BR-W-5).
func (h *WebhookProxyHandler) Handle(providerName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if h.baseURL == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// Read the EXACT bytes the provider signed — no decode (BR-W-2).
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		target := h.baseURL + "/webhooks/" + providerName
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Forward all inbound headers verbatim (carries Stripe-Signature / PayPal-*
		// transmission headers + Content-Type) so payment-svc verifies against the
		// same bytes + signature. Hop-by-hop headers are not set by net/http here.
		for k, vs := range r.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}

		resp, err := h.client.Do(req)
		if err != nil {
			// payment-svc unreachable — return 5xx so the provider redelivers.
			h.logger.WarnContext(ctx, "webhook_proxy_failed",
				slog.String("event", "webhook_proxy_failed"),
				slog.String("provider", providerName),
				slog.String("error", err.Error()),
			)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookBody))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}
}
