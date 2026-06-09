// Package stripe implements the PaymentProvider seam for Stripe (Story 7.3,
// Q-SDK). Webhook verification follows the documented Stripe-Signature scheme
// byte-for-byte — HMAC-SHA256 over `{timestamp}.{payload}` with the webhook
// signing secret, constant-time compare, timestamp-tolerance replay window —
// which is exactly what stripe-go's webhook.ConstructEvent does internally. It is
// implemented over the stdlib (crypto/hmac) so payment-svc has no third-party SDK
// dependency and the adversarial lanes (forged / stale / tampered) run fully
// offline; the seam isolates a future stripe-go swap.
//
// CreateCheckout / CreateSubscription call the Stripe REST API over net/http
// (form-encoded), with the base URL + HTTP client + clock injectable for tests.
// The secret key + webhook signing secret are env-injected and NEVER logged
// (distinct secrets, distinct blast radius — Q-SECRETS, BR-W-6).
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// DefaultBaseURL is the Stripe REST API base.
const DefaultBaseURL = "https://api.stripe.com"

// DefaultTolerance bounds replay: a signature whose timestamp is older than this
// (or in the future by this much) is rejected (BR-W-4). Matches Stripe's default.
const DefaultTolerance = 5 * time.Minute

// metadataOrderKey is the metadata field carrying OUR order id into the provider
// so the webhook resolves our order, never a client value (BR-R-4).
const metadataOrderKey = "he_order_id"

// Provider implements provider.PaymentProvider for Stripe.
type Provider struct {
	secretKey     string // sk_... — REST auth (NEVER logged)
	webhookSecret string // whsec_... — webhook HMAC (NEVER logged; distinct from secretKey)
	baseURL       string
	client        *http.Client
	tolerance     time.Duration
	now           func() time.Time
}

// Option configures a Provider (tests inject baseURL / client / clock).
type Option func(*Provider)

// WithBaseURL overrides the Stripe API base (tests point at httptest).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// WithClock overrides the clock (tests pin `now` for deterministic tolerance).
func WithClock(now func() time.Time) Option { return func(p *Provider) { p.now = now } }

// WithTolerance overrides the replay tolerance window.
func WithTolerance(d time.Duration) Option { return func(p *Provider) { p.tolerance = d } }

// New constructs a Stripe Provider. secretKey + webhookSecret are env-injected.
func New(secretKey, webhookSecret string, opts ...Option) *Provider {
	p := &Provider{
		secretKey:     secretKey,
		webhookSecret: webhookSecret,
		baseURL:       DefaultBaseURL,
		client:        &http.Client{Timeout: 15 * time.Second},
		tolerance:     DefaultTolerance,
		now:           time.Now,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical provider id.
func (p *Provider) Name() string { return "stripe" }

// VerifyWebhook verifies the Stripe-Signature header against the EXACT raw bytes
// and normalises the event. On ANY signature failure it returns
// provider.ErrSignatureInvalid and parses NOTHING into a money action (BR-W-1).
func (p *Provider) VerifyWebhook(_ context.Context, rawBody []byte, headers http.Header) (provider.VerifiedEvent, error) {
	sigHeader := headers.Get("Stripe-Signature")
	if sigHeader == "" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	ts, sigs := parseSigHeader(sigHeader)
	if ts == 0 || len(sigs) == 0 {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Replay window: reject a stale (or far-future) timestamp BEFORE any compare.
	age := p.now().Unix() - ts
	if age < 0 {
		age = -age
	}
	if time.Duration(age)*time.Second > p.tolerance {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// HMAC-SHA256 over `{timestamp}.{payload}` with the webhook signing secret.
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(rawBody)
	expected := mac.Sum(nil)
	matched := false
	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err != nil {
			continue
		}
		if hmac.Equal(got, expected) { // constant-time compare
			matched = true
			break
		}
	}
	if !matched {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Signature OK — NOW it is safe to parse the body into an event.
	return parseEvent(rawBody)
}

// parseSigHeader extracts the timestamp (t=) and v1 signatures from a
// Stripe-Signature header: `t=1492774577,v1=abc...,v1=def...`.
func parseSigHeader(h string) (int64, []string) {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(h, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, _ = strconv.ParseInt(kv[1], 10, 64)
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	return ts, sigs
}

// stripeEnvelope is the minimal slice of a Stripe event we read.
type stripeEnvelope struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID             string            `json:"id"`
			PaymentIntent  string            `json:"payment_intent"`
			Subscription   string            `json:"subscription"`
			Currency       string            `json:"currency"`
			AmountTotal    *int64            `json:"amount_total"`
			Amount         *int64            `json:"amount"`
			AmountReceived *int64            `json:"amount_received"`
			AmountPaid     *int64            `json:"amount_paid"`
			Metadata       map[string]string `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

// parseEvent normalises a verified Stripe event into a VerifiedEvent. Unknown /
// no-op event types map to EventUnhandled (200-ACK no-op downstream, Q-REFUND).
func parseEvent(rawBody []byte) (provider.VerifiedEvent, error) {
	var env stripeEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		// Signature already verified — a body we can't parse is a contract error,
		// not a forgery; surface it so the caller can 400 without crediting.
		return provider.VerifiedEvent{}, fmt.Errorf("stripe: parse event: %w", err)
	}
	obj := env.Data.Object
	ev := provider.VerifiedEvent{
		Provider:               "stripe",
		ExternalSubscriptionID: obj.Subscription,
		Currency:               strings.ToUpper(obj.Currency),
		SettledAmount:          minorToDecimal(firstNonNil(obj.AmountTotal, obj.AmountReceived, obj.Amount, obj.AmountPaid)),
	}
	if obj.Metadata != nil {
		ev.OrderID = obj.Metadata[metadataOrderKey]
	}
	switch env.Type {
	case "checkout.session.completed", "payment_intent.succeeded":
		ev.Kind = provider.EventRechargePaid
		ev.Status = "paid"
		ev.ExternalOrderID = firstNonEmpty(obj.PaymentIntent, obj.ID)
	case "payment_intent.payment_failed", "checkout.session.expired":
		ev.Kind = provider.EventRechargeFailed
		ev.Status = "failed"
		ev.ExternalOrderID = firstNonEmpty(obj.PaymentIntent, obj.ID)
	case "invoice.paid", "invoice.payment_succeeded":
		ev.Kind = provider.EventSubscriptionRenew
		ev.Status = "active"
		ev.ExternalOrderID = obj.ID
	case "customer.subscription.created", "customer.subscription.updated":
		ev.Kind = provider.EventSubscriptionActive
		ev.Status = "active"
		ev.ExternalSubscriptionID = firstNonEmpty(obj.Subscription, obj.ID)
	case "invoice.payment_failed":
		ev.Kind = provider.EventSubscriptionPastDue
		ev.Status = "past_due"
	case "customer.subscription.deleted":
		ev.Kind = provider.EventSubscriptionCancel
		ev.Status = "cancelled"
		ev.ExternalSubscriptionID = firstNonEmpty(obj.Subscription, obj.ID)
	default:
		ev.Kind = provider.EventUnhandled
	}
	return ev, nil
}

func firstNonNil(vals ...*int64) int64 {
	for _, v := range vals {
		if v != nil {
			return *v
		}
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// minorToDecimal converts an integer minor-unit amount (cents) to a 2dp
// string-decimal (Q-Spec-4). Stripe USD amounts are in cents.
func minorToDecimal(minor int64) string {
	return decimal.NewFromInt(minor).Div(decimal.NewFromInt(100)).StringFixed(2)
}

// CreateCheckout opens a Stripe Checkout session for a pending order. The order
// id is carried as session metadata so the webhook resolves OUR order (BR-R-4).
func (p *Provider) CreateCheckout(ctx context.Context, o provider.Order) (provider.CheckoutResult, error) {
	amt, err := decimal.NewFromString(o.Amount)
	if err != nil {
		return provider.CheckoutResult{}, fmt.Errorf("stripe: bad amount %q: %w", o.Amount, err)
	}
	cents := amt.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", "https://he-api.example/billing/success")
	form.Set("cancel_url", "https://he-api.example/billing/cancel")
	form.Set("client_reference_id", o.OrderID)
	form.Set("metadata["+metadataOrderKey+"]", o.OrderID)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(o.Currency))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(cents, 10))
	form.Set("line_items[0][price_data][product_data][name]", "He-API balance recharge")
	// payment_intent metadata so payment_intent.succeeded also carries our id.
	form.Set("payment_intent_data[metadata]["+metadataOrderKey+"]", o.OrderID)

	var out struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		PaymentIntent string `json:"payment_intent"`
	}
	if err := p.post(ctx, "/v1/checkout/sessions", o.OrderID, form, &out); err != nil {
		return provider.CheckoutResult{}, err
	}
	return provider.CheckoutResult{
		CheckoutURL:     out.URL,
		ExternalOrderID: firstNonEmpty(out.PaymentIntent, out.ID),
	}, nil
}

// CreateSubscription opens a Stripe Checkout session in subscription mode (RAIL
// only — the price/plan mapping is a future concern, Q-SUBSCOPE).
func (p *Provider) CreateSubscription(ctx context.Context, s provider.Subscription) (provider.SubscriptionResult, error) {
	form := url.Values{}
	form.Set("mode", "subscription")
	form.Set("success_url", "https://he-api.example/billing/success")
	form.Set("cancel_url", "https://he-api.example/billing/cancel")
	form.Set("client_reference_id", s.SubscriptionID)
	form.Set("metadata[he_subscription_id]", s.SubscriptionID)
	form.Set("metadata[plan]", s.Plan)

	var out struct {
		ID           string `json:"id"`
		URL          string `json:"url"`
		Subscription string `json:"subscription"`
	}
	if err := p.post(ctx, "/v1/checkout/sessions", s.SubscriptionID, form, &out); err != nil {
		return provider.SubscriptionResult{}, err
	}
	return provider.SubscriptionResult{
		CheckoutURL:            out.URL,
		ExternalSubscriptionID: firstNonEmpty(out.Subscription, out.ID),
	}, nil
}

// post issues a form-encoded Stripe REST call with bearer auth + an idempotency
// key (BR-R-6: a client double-submit does not create two provider charges).
func (p *Provider) post(ctx context.Context, path, idempotencyKey string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("stripe: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.secretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// NEVER embed the secret/URL in the surfaced error.
		return fmt.Errorf("stripe: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stripe: provider returned status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("stripe: decode response: %w", err)
	}
	return nil
}
