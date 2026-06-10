// Package alipay implements the PaymentProvider seam for the Alipay+ / Antom
// international cashier (Story 7.5, Q-SDK — a THIN REST client on the platform's
// standard net/http + stdlib crypto, mirroring the 7.3 PayPal / 7.4 Coinbase
// decisions: no third-party SDK, so the adversarial lanes run fully offline). It
// plugs into the seam Story 7.3 stood up WITHOUT touching the interface, the
// webhook/recharge handlers, the producer, or the credit applier (provider.go:2-6
// names 7.5 by name).
//
// Two genuinely-new primitives vs the 7.3/7.4 spine:
//
//   - The platform's FIRST ASYMMETRIC webhook signature scheme. Antom signs
//     notifications RSA2 (SHA256withRSA) with ITS private key; we verify with
//     Alipay+'s PUBLIC key — we never hold a shared secret, so the channel cannot
//     be forged from our config alone (a public key is not a secret). The signed
//     content is a CONSTRUCTED string `POST <publicNotifyPath>\n<Client-Id>.
//     <Request-Time>.<rawBody>`, NOT the raw body alone (Stripe/Coinbase were
//     symmetric HMAC over the body). Reconstruction uses the CONFIGURED public
//     notify path (Q-NOTIFY-PATH), NOT r.URL.Path (the gateway proxies to a
//     different internal path). Request-Time gives a freshness window → a stale
//     notification is rejected (the signature-timestamp replay layer Coinbase
//     lacked, RESTORED — on par with Stripe; BR-W-4).
//   - Minor-unit integer amounts: Antom paymentAmount.value is an integer in the
//     currency's minor units ("5000" = 50.00). A per-currency minor-unit table
//     (NOT a hardcoded /100) converts to/from the platform string-decimal, and a
//     scale loss fails CLOSED (no mis-scaled 100× credit; BR-W-7 / Q-AMOUNT-UNITS).
//
// We sign OUR outbound Antom calls with our MERCHANT PRIVATE KEY over the same
// constructed-string shape. ⚠️ That private key authenticates ALL our outbound
// calls — a broader blast radius than an HMAC webhook secret — so it is
// env-injected and NEVER logged (Q-SECRETS, BR-W-6). Alipay+ is a wallet channel:
// the user authenticates in their wallet on Antom's hosted cashier, so no card PAN
// transits He-API (SAQ-A boundary holds, BR-W-8).
package alipay

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// DefaultBaseURL is the Antom / Alipay+ Open API base (sandbox/prod via WithBaseURL).
const DefaultBaseURL = "https://open-na.alipay.com"

// payPath is the Antom Cashier Payment create endpoint.
const payPath = "/ams/api/v1/payments/pay"

// DefaultNotifyPath is the PUBLIC webhook path Antom POSTs to + signs over. The
// signed-string reconstruction uses THIS (Q-NOTIFY-PATH), never the internal
// proxied r.URL.Path — overridable via WithNotifyPath.
const DefaultNotifyPath = "/v1/billing/webhooks/alipay"

// DefaultTolerance bounds replay: a notification whose Request-Time is older than
// this (or in the future by this much) is rejected (BR-W-4). Matches the Stripe
// default tolerance.
const DefaultTolerance = 5 * time.Minute

// signAlgorithm is the only signature algorithm we accept on the Signature header.
const signAlgorithm = "RSA256"

// metadataOrderKey / metadataUserKey carry OUR ids into the Antom charge so the
// notification resolves OUR order, never a client-asserted user_id (BR-A-3).
const (
	metadataOrderKey = "order_id"
	metadataUserKey  = "user_id"
)

// minorUnitDigits is the per-currency minor-unit exponent (Q-AMOUNT-UNITS). USD
// and CNY are both 2-minor-digit. A table — NOT a hardcoded /100 — keeps the
// conversion correct if a zero-decimal currency is ever enabled.
var minorUnitDigits = map[string]int32{"USD": 2, "CNY": 2}

// Provider implements provider.PaymentProvider for Alipay+ / Antom.
type Provider struct {
	clientID   string          // Client-Id — identifies us to Antom (NOT a secret)
	privateKey *rsa.PrivateKey // signs OUR outbound calls (⚠️ broad blast radius; NEVER logged)
	publicKey  *rsa.PublicKey  // verifies INBOUND notifications (Alipay+'s public key)
	keyErr     error           // PEM parse failure → every op fails CLOSED

	baseURL    string
	notifyPath string // PUBLIC notify path for signed-string reconstruction (Q-NOTIFY-PATH)
	keyVersion string // keyVersion stamped on OUR outbound Signature header (rotation)
	client     *http.Client
	tolerance  time.Duration
	now        func() time.Time
}

// Option configures a Provider (tests inject baseURL / notify-path / clock).
type Option func(*Provider)

// WithBaseURL overrides the Antom API base (tests point at httptest).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithNotifyPath overrides the PUBLIC notify path used to reconstruct the signed
// string (Q-NOTIFY-PATH). MUST match the URL Antom actually POSTs to + signs.
func WithNotifyPath(path string) Option { return func(p *Provider) { p.notifyPath = path } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// WithClock overrides the clock (tests pin `now` for deterministic freshness).
func WithClock(now func() time.Time) Option { return func(p *Provider) { p.now = now } }

// WithTolerance overrides the Request-Time freshness window.
func WithTolerance(d time.Duration) Option { return func(p *Provider) { p.tolerance = d } }

// WithKeyVersion sets the keyVersion stamped on OUR outbound Signature header.
func WithKeyVersion(v string) Option { return func(p *Provider) { p.keyVersion = v } }

// New constructs an Alipay+ / Antom Provider. clientID + merchantPrivateKeyPEM +
// alipayPublicKeyPEM are env-injected. A PEM parse failure is retained in keyErr
// and surfaces (fail-closed) on first use rather than panicking at boot — a
// malformed verify key then REJECTS every notification (no forged credit), and a
// malformed signing key fails outbound checkout creation.
func New(clientID, merchantPrivateKeyPEM, alipayPublicKeyPEM string, opts ...Option) *Provider {
	p := &Provider{
		clientID:   clientID,
		baseURL:    DefaultBaseURL,
		notifyPath: DefaultNotifyPath,
		keyVersion: "1",
		client:     &http.Client{Timeout: 15 * time.Second},
		tolerance:  DefaultTolerance,
		now:        time.Now,
	}
	if priv, err := parsePrivateKey(merchantPrivateKeyPEM); err != nil {
		p.keyErr = err
	} else {
		p.privateKey = priv
	}
	if p.keyErr == nil {
		if pub, err := parsePublicKey(alipayPublicKeyPEM); err != nil {
			p.keyErr = err
		} else {
			p.publicKey = pub
		}
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical provider id (the Alipay+/Antom INTEGRATION/aggregator,
// mirroring stripe/paypal/coinbase — the specific wallet is chosen on Antom's
// hosted cashier, Q-WALLET / BR-A-6).
func (p *Provider) Name() string { return "alipay" }

// CreateCheckout creates an Antom Cashier Payment for a pending order and returns
// the hosted cashier URL + Antom paymentId. The request is RSA-signed with our
// merchant private key over the constructed-string shape; OUR order id rides in
// paymentRequestId + metadata so the notification resolves OUR order (BR-A-3).
func (p *Provider) CreateCheckout(ctx context.Context, o provider.Order) (provider.CheckoutResult, error) {
	if p.keyErr != nil || p.privateKey == nil {
		// Fail-closed: never claim a checkout we cannot sign (no key leak in the error).
		return provider.CheckoutResult{}, fmt.Errorf("alipay: signing key unavailable")
	}
	minor, err := decimalToMinor(o.Amount, o.Currency)
	if err != nil {
		return provider.CheckoutResult{}, err
	}

	reqBody := map[string]any{
		"paymentRequestId": o.OrderID, // OUR order id — resolves the notification (BR-A-3)
		"order": map[string]any{
			"orderDescription": "He-API balance recharge",
			"referenceOrderId": o.OrderID,
		},
		"paymentAmount": map[string]string{
			"currency": strings.ToUpper(strings.TrimSpace(o.Currency)),
			"value":    minor, // minor-unit integer string (Q-AMOUNT-UNITS)
		},
		"paymentRedirectUrl": "https://he-api.example/billing/success",
		"paymentNotifyUrl":   p.notifyPath,
		"metadata": map[string]string{
			metadataOrderKey: o.OrderID,
			metadataUserKey:  o.UserID,
		},
	}

	var out struct {
		PaymentID         string `json:"paymentId"`
		NormalURL         string `json:"normalUrl"`
		PaymentActionForm string `json:"paymentActionForm"`
		Result            struct {
			ResultStatus string `json:"resultStatus"`
			ResultCode   string `json:"resultCode"`
		} `json:"result"`
	}
	if err := p.postSigned(ctx, payPath, reqBody, &out); err != nil {
		return provider.CheckoutResult{}, err
	}
	checkoutURL := firstNonEmpty(out.NormalURL, out.PaymentActionForm)
	if checkoutURL == "" || out.PaymentID == "" {
		// A 2xx with no usable cashier handle is a contract error — never claim a
		// half-built checkout (BLIND-ERROR-004).
		return provider.CheckoutResult{}, fmt.Errorf("alipay: pay response missing paymentId/checkout url")
	}
	return provider.CheckoutResult{
		CheckoutURL:     checkoutURL,
		ExternalOrderID: out.PaymentID,
	}, nil
}

// CreateSubscription is unsupported: Alipay+ Auto Debit needs a separate
// agreement-binding flow (subscription tiers are Story 7.8, Q-SUBSCOPE). Returns a
// not-supported error so the seam stays total without faking a subscription
// (mirrors coinbase).
func (p *Provider) CreateSubscription(_ context.Context, _ provider.Subscription) (provider.SubscriptionResult, error) {
	return provider.SubscriptionResult{}, fmt.Errorf("alipay: subscriptions are not supported (Auto Debit out of scope for 7.5)")
}

// VerifyWebhook verifies the Alipay+ RSA2 signature over the reconstructed
// constructed string and normalises the event. On ANY signature/freshness failure
// it returns provider.ErrSignatureInvalid and parses NOTHING into a money action
// (BR-W-1). Reconstruction uses the CONFIGURED public notify path, NOT r.URL.Path
// (Q-NOTIFY-PATH); the method is fixed POST.
func (p *Provider) VerifyWebhook(_ context.Context, rawBody []byte, headers http.Header) (provider.VerifiedEvent, error) {
	if p.keyErr != nil || p.publicKey == nil {
		// Fail-closed: a missing/garbage verify key rejects everything (no forged credit).
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	sigHeader := headers.Get("Signature")
	clientID := headers.Get("Client-Id")
	requestTime := headers.Get("Request-Time")
	if sigHeader == "" || clientID == "" || requestTime == "" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Client-Id must match the configured merchant (anti cross-merchant replay). A
	// tampered Client-Id also breaks the signature below — this is belt-and-suspenders.
	if clientID != p.clientID {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Replay layer 1 — Request-Time freshness: reject a stale (or far-future) time
	// BEFORE any verify (BR-W-4). The signature-timestamp layer Coinbase lacked.
	rt, err := time.Parse(time.RFC3339, strings.TrimSpace(requestTime))
	if err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	age := p.now().Sub(rt)
	if age < 0 {
		age = -age
	}
	if age > p.tolerance {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	sigB64, ok := parseSignatureHeader(sigHeader)
	if !ok {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Reconstruct EXACTLY: `POST <publicNotifyPath>\n<Client-Id>.<Request-Time>.<rawBody>`.
	signStr := signingString(http.MethodPost, p.notifyPath, clientID, requestTime, rawBody)
	digest := sha256.Sum256([]byte(signStr))
	if err := rsa.VerifyPKCS1v15(p.publicKey, crypto.SHA256, digest[:], sig); err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Signature OK — NOW it is safe to parse the body into an event.
	return parseNotification(rawBody)
}

// signingString builds the Antom constructed signing string:
// `<METHOD> <uri>\n<clientId>.<requestTime>.<body>`.
func signingString(method, uri, clientID, requestTime string, body []byte) string {
	var b strings.Builder
	b.Grow(len(method) + len(uri) + len(clientID) + len(requestTime) + len(body) + 4)
	b.WriteString(method)
	b.WriteByte(' ')
	b.WriteString(uri)
	b.WriteByte('\n')
	b.WriteString(clientID)
	b.WriteByte('.')
	b.WriteString(requestTime)
	b.WriteByte('.')
	b.Write(body)
	return b.String()
}

// parseSignatureHeader extracts the base64 signature from an Antom Signature
// header `algorithm=RSA256,keyVersion=<v>,signature=<urlencoded-base64>`. Only
// algorithm=RSA256 is accepted; the signature is URL-decoded. keyVersion is
// parsed but, for the MVP single-key config, not used to select a key.
func parseSignatureHeader(h string) (string, bool) {
	var algorithm, sig string
	seen := false
	for _, part := range strings.Split(h, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "algorithm":
			algorithm = strings.TrimSpace(kv[1])
		case "signature":
			seen = true
			// The signature value is URL-encoded base64. QueryUnescape decodes %XX
			// and "+" — but a base64 "+" must survive, so unescape only %-escapes.
			if dec, err := url.QueryUnescape(strings.TrimSpace(kv[1])); err == nil {
				sig = dec
			} else {
				sig = strings.TrimSpace(kv[1])
			}
		}
	}
	if !seen || algorithm != signAlgorithm || sig == "" {
		return "", false
	}
	return sig, true
}

// notification is the minimal slice of an Antom payment notification we read.
type notification struct {
	Result struct {
		ResultStatus string `json:"resultStatus"`
	} `json:"result"`
	PaymentID        string `json:"paymentId"`
	PaymentRequestID string `json:"paymentRequestId"`
	PaymentAmount    struct {
		Currency string `json:"currency"`
		Value    string `json:"value"`
	} `json:"paymentAmount"`
}

// parseNotification normalises a verified Antom notification into a VerifiedEvent.
// SettledAmount is the PROVIDER-CONFIRMED settled value (minor-unit→string-decimal),
// never the create-order intent (BR-C-3). resultStatus gates the credit (Q-CONFIRM):
// S→paid, F→failed, U/unknown→EventUnhandled (200-ACK no-op, no credit).
func parseNotification(rawBody []byte) (provider.VerifiedEvent, error) {
	var n notification
	if err := json.Unmarshal(rawBody, &n); err != nil {
		// Signature already verified — an unparseable body is a contract error, not a
		// forgery; surface it so the handler 400s without crediting.
		return provider.VerifiedEvent{}, fmt.Errorf("alipay: parse notification: %w", err)
	}
	ev := provider.VerifiedEvent{
		Provider:        "alipay",
		OrderID:         n.PaymentRequestID, // OUR order id — never a client user_id (BR-A-3)
		ExternalOrderID: n.PaymentID,
		Currency:        strings.ToUpper(strings.TrimSpace(n.PaymentAmount.Currency)),
	}
	switch strings.ToUpper(strings.TrimSpace(n.Result.ResultStatus)) {
	case "S":
		// Antom finality → credit. Convert the minor-unit value to a string-decimal;
		// a scale failure fails CLOSED (no mis-scaled 100× credit, BR-W-7).
		settled, err := minorToDecimal(n.PaymentAmount.Value, n.PaymentAmount.Currency)
		if err != nil {
			return provider.VerifiedEvent{}, fmt.Errorf("alipay: settled amount: %w", err)
		}
		ev.SettledAmount = settled
		ev.Kind = provider.EventRechargePaid
		ev.Status = "paid"
	case "F":
		ev.Kind = provider.EventRechargeFailed
		ev.Status = "failed"
	default:
		// "U" (in-progress/unknown) or any unknown status → 200-ACK no-op, NO credit
		// (crediting a non-terminal result risks crediting a payment that never
		// completes, Q-CONFIRM / BR-C-1).
		ev.Kind = provider.EventUnhandled
	}
	return ev, nil
}

// decimalToMinor converts a string-decimal amount to an Antom integer minor-unit
// string for currency (Q-AMOUNT-UNITS). Fails CLOSED on sub-minor precision (a
// scale loss would mis-scale the charge). "50.00"/USD → "5000".
func decimalToMinor(amount, currency string) (string, error) {
	digits, ok := minorUnitDigits[strings.ToUpper(strings.TrimSpace(currency))]
	if !ok {
		return "", fmt.Errorf("alipay: unsupported currency %q", currency)
	}
	d, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return "", fmt.Errorf("alipay: bad amount %q", amount)
	}
	scaled := d.Shift(digits) // ×10^digits
	if !scaled.Equal(scaled.Truncate(0)) {
		// Sub-minor precision (e.g. "50.001" USD) → reject rather than silently lose it.
		return "", fmt.Errorf("alipay: amount %q has sub-minor precision for %s", amount, currency)
	}
	return scaled.Truncate(0).String(), nil
}

// minorToDecimal converts an Antom integer minor-unit string to a string-decimal
// for currency (Q-AMOUNT-UNITS). Fails CLOSED on a non-integer minor value. "5000"/
// USD → "50.00".
func minorToDecimal(value, currency string) (string, error) {
	digits, ok := minorUnitDigits[strings.ToUpper(strings.TrimSpace(currency))]
	if !ok {
		return "", fmt.Errorf("alipay: unsupported currency %q", currency)
	}
	v, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("alipay: bad minor-unit value %q", value)
	}
	if !v.Equal(v.Truncate(0)) {
		// Minor units are integers; a fractional value is a contract violation.
		return "", fmt.Errorf("alipay: minor-unit value %q is not an integer", value)
	}
	return v.Shift(-digits).StringFixed(digits), nil
}

// postSigned issues a JSON Antom REST call signed with our merchant private key
// over the constructed-string shape. The surfaced error NEVER embeds the key /
// url / body (BR-W-6).
func (p *Provider) postSigned(ctx context.Context, path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("alipay: marshal request")
	}
	requestTime := p.now().UTC().Format(time.RFC3339)
	signStr := signingString(http.MethodPost, path, p.clientID, requestTime, buf)
	digest := sha256.Sum256([]byte(signStr))
	raw, err := rsa.SignPKCS1v15(rand.Reader, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return fmt.Errorf("alipay: sign request")
	}
	sigHeader := fmt.Sprintf("algorithm=%s,keyVersion=%s,signature=%s",
		signAlgorithm, p.keyVersion, url.QueryEscape(base64.StdEncoding.EncodeToString(raw)))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("alipay: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Client-Id", p.clientID)
	req.Header.Set("Request-Time", requestTime)
	req.Header.Set("Signature", sigHeader)
	resp, err := p.client.Do(req)
	if err != nil {
		// NEVER embed the secret/URL in the surfaced error.
		return fmt.Errorf("alipay: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alipay: provider returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("alipay: decode response")
	}
	return nil
}

// parsePrivateKey parses an RSA private key from a PEM block (PKCS#8 or PKCS#1) or
// a raw base64 DER body. Returns a generic error (no key material) on failure.
func parsePrivateKey(s string) (*rsa.PrivateKey, error) {
	der, err := pemOrBase64(s)
	if err != nil {
		return nil, fmt.Errorf("alipay: parse private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
	}
	return nil, fmt.Errorf("alipay: unsupported private key format")
}

// parsePublicKey parses an RSA public key from a PEM block (PKIX or PKCS#1) or a
// raw base64 DER body. Returns a generic error on failure.
func parsePublicKey(s string) (*rsa.PublicKey, error) {
	der, err := pemOrBase64(s)
	if err != nil {
		return nil, fmt.Errorf("alipay: parse public key")
	}
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return k, nil
	}
	return nil, fmt.Errorf("alipay: unsupported public key format")
}

// pemOrBase64 returns the DER bytes from a PEM block if present, else base64-decodes
// the trimmed string (Antom often distributes keys as bare base64).
func pemOrBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty key")
	}
	if block, _ := pem.Decode([]byte(s)); block != nil {
		return block.Bytes, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
