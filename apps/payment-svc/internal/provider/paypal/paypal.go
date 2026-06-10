// Package paypal implements the PaymentProvider seam for PayPal (Story 7.3,
// Q-SDK — Architect ruling: PayPal's official Go SDK is effectively unmaintained,
// so build a THIN REST client on the platform's standard net/http). Webhook
// verification uses PayPal's /v1/notifications/verify-webhook-signature endpoint
// (transmission id/time/cert-url/auth-algo + the raw event) — the provider is the
// signature authority. The base URL + HTTP client are injectable so the
// adversarial parity lanes run against an httptest stub offline.
//
// The client id / secret / webhook id are env-injected and NEVER logged
// (Q-SECRETS, BR-W-6).
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// DefaultBaseURL is the PayPal REST API base (live). Sandbox is
// https://api-m.sandbox.paypal.com (env-selected via the *_TEST secret variant).
// PayPal's resource.custom_id carries OUR order id (BR-R-4).
const DefaultBaseURL = "https://api-m.paypal.com"

// Provider implements provider.PaymentProvider for PayPal.
type Provider struct {
	clientID     string // env-injected (NEVER logged)
	clientSecret string // env-injected (NEVER logged)
	webhookID    string // the configured webhook id verification binds to
	baseURL      string
	client       *http.Client
}

// Option configures a Provider (tests inject baseURL / client).
type Option func(*Provider)

// WithBaseURL overrides the PayPal API base (tests point at httptest).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// New constructs a PayPal Provider. All three credentials are env-injected.
func New(clientID, clientSecret, webhookID string, opts ...Option) *Provider {
	p := &Provider{
		clientID:     clientID,
		clientSecret: clientSecret,
		webhookID:    webhookID,
		baseURL:      DefaultBaseURL,
		client:       &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical provider id.
func (p *Provider) Name() string { return "paypal" }

// verifyRequest is the body of /v1/notifications/verify-webhook-signature.
type verifyRequest struct {
	AuthAlgo         string          `json:"auth_algo"`
	CertURL          string          `json:"cert_url"`
	TransmissionID   string          `json:"transmission_id"`
	TransmissionSig  string          `json:"transmission_sig"`
	TransmissionTime string          `json:"transmission_time"`
	WebhookID        string          `json:"webhook_id"`
	WebhookEvent     json.RawMessage `json:"webhook_event"`
}

// VerifyWebhook verifies the PayPal transmission signature via the provider's
// verify endpoint, then normalises the event. On any verification failure (or a
// non-SUCCESS verdict) it returns provider.ErrSignatureInvalid and parses
// nothing into a money action (BR-W-1).
func (p *Provider) VerifyWebhook(ctx context.Context, rawBody []byte, headers http.Header) (provider.VerifiedEvent, error) {
	vr := verifyRequest{
		AuthAlgo:         headers.Get("Paypal-Auth-Algo"),
		CertURL:          headers.Get("Paypal-Cert-Url"),
		TransmissionID:   headers.Get("Paypal-Transmission-Id"),
		TransmissionSig:  headers.Get("Paypal-Transmission-Sig"),
		TransmissionTime: headers.Get("Paypal-Transmission-Time"),
		WebhookID:        p.webhookID,
		WebhookEvent:     json.RawMessage(rawBody),
	}
	if vr.TransmissionID == "" || vr.TransmissionSig == "" || vr.CertURL == "" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	token, err := p.accessToken(ctx)
	if err != nil {
		// Can't reach the signature authority → treat as unverifiable (reject).
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	var verifyResp struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := p.postJSON(ctx, "/v1/notifications/verify-webhook-signature", token, vr, &verifyResp); err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	if verifyResp.VerificationStatus != "SUCCESS" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	return parseEvent(rawBody)
}

// paypalEnvelope is the minimal slice of a PayPal webhook event we read.
type paypalEnvelope struct {
	ID        string `json:"id"`
	EventType string `json:"event_type"`
	Resource  struct {
		ID                 string `json:"id"`
		CustomID           string `json:"custom_id"`
		BillingAgreementID string `json:"billing_agreement_id"`
		Status             string `json:"status"`
		Amount             struct {
			Value        string `json:"value"`
			CurrencyCode string `json:"currency_code"`
			Total        string `json:"total"`    // older v1 shape
			Currency     string `json:"currency"` // older v1 shape
		} `json:"amount"`
		CustomField string `json:"custom"` // v1 subscription custom field
	} `json:"resource"`
}

// parseEvent normalises a verified PayPal event. Unknown / no-op types map to
// EventUnhandled (refund/dispute 200-ACK no-op, Q-REFUND).
func parseEvent(rawBody []byte) (provider.VerifiedEvent, error) {
	var env paypalEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return provider.VerifiedEvent{}, fmt.Errorf("paypal: parse event: %w", err)
	}
	res := env.Resource
	ev := provider.VerifiedEvent{
		Provider:        "paypal",
		OrderID:         firstNonEmpty(res.CustomID, res.CustomField),
		ExternalOrderID: res.ID,
		SettledAmount:   firstNonEmpty(res.Amount.Value, res.Amount.Total),
		Currency:        strings.ToUpper(firstNonEmpty(res.Amount.CurrencyCode, res.Amount.Currency)),
	}
	switch env.EventType {
	case "PAYMENT.CAPTURE.COMPLETED", "CHECKOUT.ORDER.APPROVED", "PAYMENT.SALE.COMPLETED":
		ev.Kind = provider.EventRechargePaid
		ev.Status = "paid"
	case "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.DECLINED", "CHECKOUT.ORDER.VOIDED":
		ev.Kind = provider.EventRechargeFailed
		ev.Status = "failed"
	case "BILLING.SUBSCRIPTION.ACTIVATED", "BILLING.SUBSCRIPTION.CREATED":
		ev.Kind = provider.EventSubscriptionActive
		ev.Status = "active"
		ev.ExternalSubscriptionID = res.ID
	case "BILLING.SUBSCRIPTION.RE-ACTIVATED", "PAYMENT.SALE.COMPLETED.SUBSCRIPTION":
		ev.Kind = provider.EventSubscriptionRenew
		ev.Status = "active"
		ev.ExternalSubscriptionID = firstNonEmpty(res.BillingAgreementID, res.ID)
	case "BILLING.SUBSCRIPTION.PAYMENT.FAILED":
		ev.Kind = provider.EventSubscriptionPastDue
		ev.Status = "past_due"
		ev.ExternalSubscriptionID = res.ID
	case "BILLING.SUBSCRIPTION.CANCELLED", "BILLING.SUBSCRIPTION.EXPIRED":
		ev.Kind = provider.EventSubscriptionCancel
		ev.Status = "cancelled"
		ev.ExternalSubscriptionID = res.ID
	default:
		ev.Kind = provider.EventUnhandled
	}
	return ev, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// CreateCheckout creates a PayPal order (intent=CAPTURE) and returns the approve
// link. custom_id carries OUR order id so the webhook resolves our order.
func (p *Provider) CreateCheckout(ctx context.Context, o provider.Order) (provider.CheckoutResult, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return provider.CheckoutResult{}, err
	}
	reqBody := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []map[string]any{{
			"custom_id": o.OrderID,
			"amount": map[string]string{
				"currency_code": strings.ToUpper(o.Currency),
				"value":         o.Amount,
			},
		}},
	}
	var out struct {
		ID    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := p.postJSONKeyed(ctx, "/v2/checkout/orders", token, o.OrderID, reqBody, &out); err != nil {
		return provider.CheckoutResult{}, err
	}
	return provider.CheckoutResult{
		CheckoutURL:     approveLink(out.Links),
		ExternalOrderID: out.ID,
	}, nil
}

// CreateSubscription creates a PayPal subscription (RAIL only — plan id mapping
// is a future concern; here `plan` is passed through as plan_id, Q-SUBSCOPE).
func (p *Provider) CreateSubscription(ctx context.Context, s provider.Subscription) (provider.SubscriptionResult, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return provider.SubscriptionResult{}, err
	}
	reqBody := map[string]any{
		"plan_id":   s.Plan,
		"custom_id": s.SubscriptionID,
	}
	var out struct {
		ID    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
	}
	if err := p.postJSONKeyed(ctx, "/v1/billing/subscriptions", token, s.SubscriptionID, reqBody, &out); err != nil {
		return provider.SubscriptionResult{}, err
	}
	return provider.SubscriptionResult{
		CheckoutURL:            approveLink(out.Links),
		ExternalSubscriptionID: out.ID,
	}, nil
}

// UpdateProviderSubscription revises an existing PayPal subscription's plan
// (Story 7.8, SubscriptionUpdater). PayPal computes any proration itself; the
// durable plan flip confirms on the 7.3 webhook (BR-S-3). prorationBehavior /
// schedule are advisory to PayPal's revise semantics (PayPal prorates by
// default; a deferred downgrade is realised by PayPal at the next cycle).
func (p *Provider) UpdateProviderSubscription(ctx context.Context, extSubID, newPlan, prorationBehavior, schedule string) (string, error) {
	if strings.TrimSpace(extSubID) == "" {
		return "", fmt.Errorf("paypal: missing subscription id")
	}
	token, err := p.accessToken(ctx)
	if err != nil {
		return "", err
	}
	reqBody := map[string]any{"plan_id": newPlan}
	var out struct {
		Status string `json:"status"`
	}
	if err := p.postJSONKeyed(ctx, "/v1/billing/subscriptions/"+url.PathEscape(extSubID)+"/revise", token, extSubID+":"+newPlan, reqBody, &out); err != nil {
		return "", err
	}
	if out.Status == "" {
		return "active", nil
	}
	return out.Status, nil
}

func approveLink(links []struct {
	Href string `json:"href"`
	Rel  string `json:"rel"`
}) string {
	for _, l := range links {
		if l.Rel == "approve" || l.Rel == "payer-action" {
			return l.Href
		}
	}
	return ""
}

// accessToken fetches an OAuth2 bearer token via client-credentials. The error
// never embeds the secret.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("paypal: build token request")
	}
	req.SetBasicAuth(p.clientID, p.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal: token request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("paypal: token status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("paypal: token decode failed")
	}
	return out.AccessToken, nil
}

func (p *Provider) postJSON(ctx context.Context, path, token string, in, out any) error {
	return p.postJSONKeyed(ctx, path, token, "", in, out)
}

// postJSONKeyed issues a JSON REST call with bearer auth + an optional
// PayPal-Request-Id idempotency key (BR-R-6).
func (p *Provider) postJSONKeyed(ctx context.Context, path, token, idempotencyKey string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("paypal: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("paypal: build request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("PayPal-Request-Id", idempotencyKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("paypal: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("paypal: provider returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("paypal: decode response: %w", err)
	}
	return nil
}
