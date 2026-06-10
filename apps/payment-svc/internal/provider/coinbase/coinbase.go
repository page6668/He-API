// Package coinbase implements the PaymentProvider seam for USDC via Coinbase
// Commerce (Story 7.4, Q-SDK — a THIN REST client on the platform's standard
// net/http, mirroring the 7.3 PayPal decision: no third-party SDK, so the
// adversarial lanes run fully offline). It plugs into the seam Story 7.3 stood up
// WITHOUT touching the interface, the webhook/recharge handlers, the producer, or
// the credit applier (provider.go:2-6).
//
// Two genuinely-new primitives vs the 7.3 fiat spine:
//
//   - The X-CC-Webhook-Signature scheme: lowercase-hex HMAC-SHA256 over the EXACT
//     raw body with COINBASE_COMMERCE_WEBHOOK_SECRET, constant-time compared
//     (hmac.Equal) BEFORE any body parsing (BR-W-1/W-2). ⚠️ NO timestamp segment
//     (unlike Stripe's t=...,v1=...) → there is NO signature-freshness window, so
//     replay defence rests ENTIRELY on the inherited recharge_orders pending→paid
//     state-machine (BR-W-4).
//   - The crypto on-chain-confirmation credit model: credit fires ONLY on
//     charge:confirmed (Coinbase's finality signal); charge:pending/created →
//     EventUnhandled (200-ACK, NO credit — unconfirmed funds are not money yet);
//     charge:failed → EventRechargeFailed; charge:delayed/underpaid → park via the
//     inherited amount-integrity guard (BR-C-1/C-3, Q-CONFIRM).
//
// The API key (charge-create auth) and the webhook secret (signature) are
// env-injected, distinct (distinct blast radius), and NEVER logged (Q-SECRETS,
// BR-W-6). Coinbase Commerce is a crypto on-chain channel — there is no card PAN
// anywhere in the flow (crypto no-PAN, stronger than SAQ-A; BR-W-7).
package coinbase

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// DefaultBaseURL is the Coinbase Commerce REST API base.
const DefaultBaseURL = "https://api.commerce.coinbase.com"

// apiVersion is the Coinbase Commerce API version header value (X-CC-Version).
const apiVersion = "2018-03-22"

// metadataOrderKey carries OUR recharge_orders.id into the Coinbase charge as
// metadata so the webhook resolves OUR order, never a client value (BR-A-3).
const metadataOrderKey = "order_id"

// metadataUserKey carries the authenticated user id (server-side; informational —
// the order→user binding is authoritative server-side, BR-A-3).
const metadataUserKey = "user_id"

// usdcEquivalents are the settlement currencies treated as USD 1:1 (USDC≈USD,
// Q-COINS / BR-A-5). A settlement outside this set + USD is left as-is so the
// inherited amount-integrity / currency guard parks it (wrong-coin, Q-COINS).
var usdcEquivalents = map[string]bool{"USDC": true, "USD": true}

// Provider implements provider.PaymentProvider for Coinbase Commerce.
type Provider struct {
	apiKey        string // X-CC-Api-Key — charge-create auth (NEVER logged)
	webhookSecret string // X-CC-Webhook-Signature HMAC secret (NEVER logged; distinct blast radius)
	baseURL       string
	client        *http.Client
}

// Option configures a Provider (tests inject baseURL / client).
type Option func(*Provider)

// WithBaseURL overrides the Coinbase Commerce API base (tests point at httptest).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// New constructs a Coinbase Commerce Provider. apiKey + webhookSecret are
// env-injected and distinct (BR-W-6).
func New(apiKey, webhookSecret string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
		baseURL:       DefaultBaseURL,
		client:        &http.Client{Timeout: 15 * time.Second},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical provider id (the INTEGRATION, mirroring
// stripe/paypal; USDC is the instrument the channel accepts — Q-COINS, BR-A-6).
func (p *Provider) Name() string { return "coinbase" }

// CreateCheckout creates a Coinbase Commerce fixed-price USDC charge for a pending
// order and returns the hosted checkout URL + charge id + the USDC deposit address.
// OUR order id rides in metadata so the webhook resolves OUR order (BR-A-3).
func (p *Provider) CreateCheckout(ctx context.Context, o provider.Order) (provider.CheckoutResult, error) {
	// Normalise the intent to a 2dp string-decimal (Coinbase local_price; Q-Spec-4,
	// no float). The handler has already validated amount > 0 (BR-A-1).
	amt, err := decimal.NewFromString(strings.TrimSpace(o.Amount))
	if err != nil {
		return provider.CheckoutResult{}, fmt.Errorf("coinbase: bad amount %q: %w", o.Amount, err)
	}

	reqBody := map[string]any{
		"name":         "He-API balance recharge",
		"description":  "He-API USDC balance recharge",
		"pricing_type": "fixed_price",
		"local_price": map[string]string{
			"amount":   amt.StringFixed(2),
			"currency": strings.ToUpper(o.Currency),
		},
		"metadata": map[string]string{
			metadataOrderKey: o.OrderID,
			metadataUserKey:  o.UserID,
		},
	}

	var out struct {
		Data chargeData `json:"data"`
	}
	if err := p.postCharge(ctx, "/charges", reqBody, &out); err != nil {
		return provider.CheckoutResult{}, err
	}
	if out.Data.HostedURL == "" || out.Data.ID == "" {
		// A 2xx with no usable charge handle is a contract error — never claim a
		// half-built checkout (BLIND-ERROR-004).
		return provider.CheckoutResult{}, fmt.Errorf("coinbase: charge response missing hosted_url/id")
	}
	return provider.CheckoutResult{
		CheckoutURL:     out.Data.HostedURL,
		ExternalOrderID: out.Data.ID,
		// The USDC deposit address (Q-ADDRESS) rides in ClientToken through the
		// existing CreateCheckoutResponse proto (no proto change) → the gateway
		// surfaces it as usdc_address. Opaque string, not a money field.
		ClientToken: out.Data.Addresses.USDC,
	}, nil
}

// CreateSubscription is unsupported: crypto has no card-on-file recurring rail
// (subscription tiers are Story 7.8). Returns a not-supported error so the seam
// stays total without faking a subscription.
func (p *Provider) CreateSubscription(_ context.Context, _ provider.Subscription) (provider.SubscriptionResult, error) {
	return provider.SubscriptionResult{}, fmt.Errorf("coinbase: subscriptions are not supported (crypto has no recurring rail)")
}

// VerifyWebhook verifies the X-CC-Webhook-Signature against the EXACT raw bytes
// and normalises the event. On ANY signature failure it returns
// provider.ErrSignatureInvalid and parses NOTHING into a money action (BR-W-1).
//
// ⚠️ The Coinbase signature carries NO timestamp → there is no replay-freshness
// window; a replayed valid charge:confirmed is made harmless SOLELY by the
// inherited recharge_orders state-machine downstream (BR-W-4).
func (p *Provider) VerifyWebhook(_ context.Context, rawBody []byte, headers http.Header) (provider.VerifiedEvent, error) {
	sig := headers.Get("X-CC-Webhook-Signature")
	if sig == "" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Lowercase-hex contract (BR-W-2): an uppercase or non-hex header is rejected
	// before the compare. Coinbase signs as lowercase hex.
	if sig != strings.ToLower(sig) {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write(rawBody)
	expected := mac.Sum(nil)
	if !hmac.Equal(got, expected) { // constant-time compare (BR-W-2; UNIT-024)
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Signature OK — NOW it is safe to parse the body into an event.
	return parseEvent(rawBody)
}

// money is a Coinbase {amount, currency} pricing/value pair (string-decimal).
type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// chargeData is the minimal slice of a Coinbase Commerce charge we read.
type chargeData struct {
	ID        string            `json:"id"`
	Code      string            `json:"code"`
	HostedURL string            `json:"hosted_url"`
	Metadata  map[string]string `json:"metadata"`
	Addresses struct {
		USDC string `json:"usdc"`
	} `json:"addresses"`
	Pricing struct {
		Local money `json:"local"`
	} `json:"pricing"`
	Payments []struct {
		Value struct {
			Local  money `json:"local"`
			Crypto money `json:"crypto"`
		} `json:"value"`
		Status string `json:"status"`
	} `json:"payments"`
}

// coinbaseEnvelope is the webhook event wrapper: {event:{type, data:{...charge...}}}.
type coinbaseEnvelope struct {
	Event struct {
		ID   string     `json:"id"`
		Type string     `json:"type"`
		Data chargeData `json:"data"`
	} `json:"event"`
}

// parseEvent normalises a verified Coinbase event into a VerifiedEvent. The
// SettledAmount is the PROVIDER-CONFIRMED on-chain value (summed payment local
// values), never the create-order intent (BR-C-3). Unknown / unconfirmed event
// types map to EventUnhandled (200-ACK no-op, no credit; Q-CONFIRM, BR-C-1).
func parseEvent(rawBody []byte) (provider.VerifiedEvent, error) {
	var env coinbaseEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		// Signature already verified — an unparseable body is a contract error, not
		// a forgery; surface it so the caller 400s without crediting.
		return provider.VerifiedEvent{}, fmt.Errorf("coinbase: parse event: %w", err)
	}
	d := env.Event.Data
	settled, currency := settledValue(d)
	ev := provider.VerifiedEvent{
		Provider:        "coinbase",
		ExternalOrderID: d.ID,
		SettledAmount:   settled,
		Currency:        currency,
	}
	if d.Metadata != nil {
		// OUR order id ONLY — never a client-asserted user_id (BR-A-3).
		ev.OrderID = d.Metadata[metadataOrderKey]
	}
	switch env.Event.Type {
	case "charge:confirmed":
		// Blockchain-confirmed = Coinbase finality → credit (reorg risk is theirs).
		ev.Kind = provider.EventRechargePaid
		ev.Status = "paid"
	case "charge:failed":
		// Expired without sufficient payment / cancelled → terminal fail, no credit.
		ev.Kind = provider.EventRechargeFailed
		ev.Status = "failed"
	default:
		// charge:created / charge:pending (unconfirmed on-chain) / charge:delayed
		// (paid late / underpaid) / charge:resolved / unknown → 200-ACK no-op, NO
		// credit (Q-CONFIRM; delayed/underpaid park via the inherited guard).
		ev.Kind = provider.EventUnhandled
	}
	return ev, nil
}

// settledValue returns the provider-confirmed settled amount (string-decimal) and
// its currency. It sums the local (fiat-equivalent) value of each on-chain payment
// (the actual settled value — enables underpay/overpay detection by the inherited
// amount-integrity guard, BR-C-3). A settlement in a non-USDC/non-USD crypto
// surfaces that crypto's currency so the inherited currency guard parks it
// (wrong-coin, Q-COINS). Falls back to the charge's local price when no payment is
// itemised yet.
func settledValue(d chargeData) (string, string) {
	if len(d.Payments) == 0 {
		return d.Pricing.Local.Amount, strings.ToUpper(d.Pricing.Local.Currency)
	}
	sum := decimal.Zero
	currency := ""
	wrongCoin := false
	for _, pay := range d.Payments {
		amt, err := decimal.NewFromString(strings.TrimSpace(pay.Value.Local.Amount))
		if err == nil {
			sum = sum.Add(amt)
		}
		if currency == "" {
			currency = strings.ToUpper(pay.Value.Local.Currency)
		}
		// A settled crypto outside the USDC-equivalent set is a wrong-coin
		// settlement — flag it so the credit applier parks rather than credits.
		if cc := strings.ToUpper(strings.TrimSpace(pay.Value.Crypto.Currency)); cc != "" && !usdcEquivalents[cc] {
			wrongCoin = true
		}
	}
	if currency == "" {
		currency = strings.ToUpper(d.Pricing.Local.Currency)
	}
	if wrongCoin {
		// Surface a non-USD currency so the inherited currency/amount guard parks
		// the credit (Q-COINS); the local-USD value is still reported for audit.
		currency = "NON_USDC"
	}
	return sum.StringFixed(2), currency
}

// postCharge issues a JSON Coinbase Commerce REST call with X-CC-Api-Key +
// X-CC-Version. The surfaced error NEVER embeds the api key / url (BR-W-6).
func (p *Provider) postCharge(ctx context.Context, path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("coinbase: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("coinbase: build request")
	}
	req.Header.Set("X-CC-Api-Key", p.apiKey)
	req.Header.Set("X-CC-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		// NEVER embed the secret/URL in the surfaced error.
		return fmt.Errorf("coinbase: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("coinbase: provider returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("coinbase: decode response: %w", err)
	}
	return nil
}
