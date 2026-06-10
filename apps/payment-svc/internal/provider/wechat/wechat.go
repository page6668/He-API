// Package wechat implements the PaymentProvider seam for WeChat Pay HK /
// cross-border (Story 7.6, Q-SDK — a THIN REST client on the platform's standard
// net/http + stdlib crypto, mirroring the 7.3 PayPal / 7.4 Coinbase / 7.5 Alipay+
// decisions: no third-party SDK, so the adversarial lanes run fully offline). It
// is the FIFTH and FINAL payment channel and plugs into the seam Story 7.3 stood
// up WITHOUT touching the interface, the webhook/recharge handlers, the producer,
// or the credit applier (provider.go:2-6 names 7.6 by name; migration 0010
// reserves the "wechat" provider string).
//
// Two genuinely-new primitives vs the 7.3/7.4/7.5 spine:
//
//   - The platform's FIRST ENCRYPTED webhook body. WeChat Pay APIv3 callbacks ship
//     the payment result as an AES-256-GCM `resource` {ciphertext, nonce,
//     associated_data} that MUST be decrypted with the APIv3Key AFTER the outer
//     signature verifies (verify-THEN-decrypt; a forged request never reaches the
//     decrypt path → no decrypt oracle). The 16-byte GCM auth tag is a SECOND
//     integrity guard under the asymmetric signature: any ciphertext/nonce/AAD
//     tampering OR a wrong APIv3Key → tag mismatch → fail-closed (NO credit, NEVER
//     a partial-plaintext parse). Stripe/Coinbase HMAC + Alipay+ RSA all signed
//     PLAINTEXT bodies; none was encrypted (Q-ENCRYPT / BR-W-3).
//   - HKD as a new fx-at-credit pair (cross-border). The inherited credit.go
//     toUSD() branch drives HKD→USD; CNY reuses the 7.5 path; USD is pass-through.
//     The minor-unit table gains HKD (2 digits).
//
// The WeChat APIv3 asymmetric signature is the SAME RSA-SHA256 family 7.5
// de-risked, with two differences: (1) the signed string is JUST
// `<Wechatpay-Timestamp>\n<Wechatpay-Nonce>\n<rawBody>\n` — NO method/URI, so the
// r.URL.Path canonicalisation trap that bit Alipay+ does NOT exist here (this is
// the ONE place 7.6 is EASIER than 7.5); (2) the verify key is selected by the
// `Wechatpay-Serial` header (platform-cert rotation) — an unknown serial → reject.
//
// We sign OUR outbound WeChat calls (CreateCheckout) with our MERCHANT PRIVATE KEY
// over `<METHOD>\n<URL>\n<timestamp>\n<nonce>\n<body>\n`. ⚠️ That private key
// authenticates ALL our outbound calls — a broad blast radius — so it is
// env-injected and NEVER logged (Q-SECRETS, BR-W-7). The APIv3Key is a NEW
// symmetric AES-256 decrypt secret no prior channel held (it decrypts every
// callback resource); it too is NEVER logged. WeChat Pay is a wallet channel: the
// user authenticates in their WeChat app by scanning the Native QR (code_url), so
// no card PAN transits He-API (SAQ-A boundary holds, BR-W-9).
package wechat

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// DefaultBaseURL is the WeChat Pay APIv3 base (HK / cross-border / sandbox via
// WithBaseURL, Q-CROSSBORDER — the HK vs global host is a base-URL config knob).
const DefaultBaseURL = "https://apihk.mch.weixin.qq.com"

// nativePath is the WeChat Pay Native (扫码支付 / QR) transaction-create endpoint.
const nativePath = "/v3/pay/transactions/native"

// DefaultNotifyURL is the PUBLIC callback URL WeChat POSTs to (overridable via
// WithNotifyURL). Unlike Alipay+, the notify path is NOT part of the signed string
// (NO method/URI → no r.URL.Path trap), so it is used only in the outbound body.
const DefaultNotifyURL = "https://he-api.example/v1/billing/webhooks/wechat"

// DefaultTolerance bounds replay: a callback whose Wechatpay-Timestamp is older
// (or in the future) than this is rejected (BR-W-5). Matches the Stripe/7.5 window.
const DefaultTolerance = 5 * time.Minute

// authSchema is the WeChat APIv3 outbound Authorization scheme.
const authSchema = "WECHATPAY2-SHA256-RSA2048"

// apiV3KeyLen is the required APIv3Key length (AES-256 → 32 bytes).
const apiV3KeyLen = 32

// minorUnitDigits is the per-currency minor-unit exponent (Q-AMOUNT-UNITS). USD,
// HKD and CNY are all 2-minor-digit. A table — NOT a hardcoded /100 — keeps the
// conversion correct if a zero-decimal currency is ever enabled. HKD is added by
// 7.6 (the first HKD pair).
var minorUnitDigits = map[string]int32{"USD": 2, "HKD": 2, "CNY": 2}

// Provider implements provider.PaymentProvider for WeChat Pay HK / cross-border.
type Provider struct {
	mchID       string          // merchant id (NOT a secret; identifies us to WeChat)
	appID       string          // WeChat appid (config; carried in the Native create body)
	privateKey  *rsa.PrivateKey // signs OUR outbound calls (⚠️ broad blast radius; NEVER logged)
	certSerial  string          // OUR merchant cert serial (outbound Authorization serial_no)
	platformKey *rsa.PublicKey  // verifies INBOUND callbacks (WeChat platform public key)
	platSerial  string          // platform cert serial — matched against Wechatpay-Serial
	apiV3Key    []byte          // ⚠️ 32B symmetric AES-256 key; decrypts EVERY callback resource (NEVER logged)
	keyErr      error           // PEM/key parse failure → every op fails CLOSED

	baseURL   string
	notifyURL string // PUBLIC callback URL sent to WeChat in CreateCheckout
	client    *http.Client
	tolerance time.Duration
	now       func() time.Time
}

// Option configures a Provider (tests inject baseURL / clock / tolerance).
type Option func(*Provider)

// WithBaseURL overrides the WeChat Pay API base (tests point at httptest; prod
// selects HK vs global, Q-CROSSBORDER).
func WithBaseURL(u string) Option { return func(p *Provider) { p.baseURL = u } }

// WithNotifyURL overrides the PUBLIC callback URL sent to WeChat (the gateway's
// public /v1/billing/webhooks/wechat route).
func WithNotifyURL(u string) Option { return func(p *Provider) { p.notifyURL = u } }

// WithAppID sets the WeChat appid carried in the Native transaction-create body.
func WithAppID(id string) Option { return func(p *Provider) { p.appID = id } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(p *Provider) { p.client = c } }

// WithClock overrides the clock (tests pin `now` for deterministic freshness).
func WithClock(now func() time.Time) Option { return func(p *Provider) { p.now = now } }

// WithTolerance overrides the Wechatpay-Timestamp freshness window.
func WithTolerance(d time.Duration) Option { return func(p *Provider) { p.tolerance = d } }

// New constructs a WeChat Pay HK / cross-border Provider. mchID +
// merchantPrivateKeyPEM + certSerial + platformPublicKeyPEM + platformSerial +
// apiV3Key are env-injected. A PEM parse failure (or a non-32-byte APIv3Key) is
// retained in keyErr and surfaces (fail-closed) on first use rather than panicking
// at boot — a malformed verify key then REJECTS every callback (no forged credit),
// a malformed signing key fails outbound checkout creation, and a bad APIv3Key
// rejects every callback (decrypt cannot succeed).
func New(mchID, merchantPrivateKeyPEM, certSerial, platformPublicKeyPEM, platformSerial, apiV3Key string, opts ...Option) *Provider {
	p := &Provider{
		mchID:      mchID,
		certSerial: certSerial,
		platSerial: platformSerial,
		baseURL:    DefaultBaseURL,
		notifyURL:  DefaultNotifyURL,
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
		if pub, err := parsePublicKey(platformPublicKeyPEM); err != nil {
			p.keyErr = err
		} else {
			p.platformKey = pub
		}
	}
	if p.keyErr == nil {
		// The APIv3Key is used verbatim as 32 AES-256 key bytes (WeChat distributes
		// it as a 32-char string). A wrong length cannot decrypt anything → fail-closed.
		if len([]byte(apiV3Key)) != apiV3KeyLen {
			p.keyErr = fmt.Errorf("wechat: APIv3 key must be %d bytes", apiV3KeyLen)
		} else {
			p.apiV3Key = []byte(apiV3Key)
		}
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name returns the canonical provider id (the WeChat Pay INTEGRATION, mirroring
// stripe/paypal/coinbase/alipay — the Native/QR product is chosen by the impl,
// Q-PRODUCT / BR-A-6).
func (p *Provider) Name() string { return "wechat" }

// CreateCheckout creates a WeChat Pay Native (QR) transaction for a pending order
// and returns the code_url (a `weixin://wxpay/bizpayurl?pr=...` string the Console
// renders as a QR). The request is RSA-signed with our merchant private key over
// the `<METHOD>\n<URL>\n<timestamp>\n<nonce>\n<body>\n` shape; OUR order id rides in
// out_trade_no so the callback resolves OUR order (BR-A-3).
func (p *Provider) CreateCheckout(ctx context.Context, o provider.Order) (provider.CheckoutResult, error) {
	if p.keyErr != nil || p.privateKey == nil {
		// Fail-closed: never claim a checkout we cannot sign (no key leak in the error).
		return provider.CheckoutResult{}, fmt.Errorf("wechat: signing key unavailable")
	}
	minor, err := decimalToMinorInt(o.Amount, o.Currency)
	if err != nil {
		return provider.CheckoutResult{}, err
	}

	reqBody := map[string]any{
		"mchid":        p.mchID,
		"appid":        p.appID,
		"description":  "He-API balance recharge",
		"out_trade_no": o.OrderID, // OUR order id — resolves the callback (BR-A-3)
		"notify_url":   p.notifyURL,
		"amount": map[string]any{
			"total":    minor, // minor-unit integer (Q-AMOUNT-UNITS)
			"currency": strings.ToUpper(strings.TrimSpace(o.Currency)),
		},
	}

	var out struct {
		CodeURL string `json:"code_url"`
	}
	if err := p.postSigned(ctx, nativePath, reqBody, &out); err != nil {
		return provider.CheckoutResult{}, err
	}
	if out.CodeURL == "" {
		// A 2xx with no code_url is a contract error — never claim a half-built
		// checkout (BLIND-ERROR).
		return provider.CheckoutResult{}, fmt.Errorf("wechat: native response missing code_url")
	}
	// The WeChat transaction_id is not known until the callback; out_trade_no is the
	// binding key. ExternalOrderID is resolved from the callback (AC2).
	return provider.CheckoutResult{CheckoutURL: out.CodeURL}, nil
}

// CreateSubscription is unsupported: WeChat 委托代扣 (entrust pay) needs a separate
// agreement-binding flow (subscription tiers are Story 7.8, Q-SUBSCOPE). Returns a
// not-supported error so the seam stays total without faking a subscription
// (mirrors coinbase/alipay).
func (p *Provider) CreateSubscription(_ context.Context, _ provider.Subscription) (provider.SubscriptionResult, error) {
	return provider.SubscriptionResult{}, fmt.Errorf("wechat: subscriptions are not supported (委托代扣 out of scope for 7.6)")
}

// VerifyWebhook verifies the WeChat APIv3 asymmetric signature AND AES-256-GCM-
// decrypts the resource, then normalises the event. The order is
// verify-THEN-decrypt-THEN-parse (BR-W-1): a forged request is rejected BEFORE any
// decrypt attempt (no decrypt oracle). On ANY signature/freshness failure it
// returns provider.ErrSignatureInvalid and parses NOTHING into a money action; on
// a decrypt/tag failure it ALSO returns ErrSignatureInvalid (a tampered ciphertext
// or wrong APIv3Key is a forgery-class failure → fail-closed, BR-W-3).
func (p *Provider) VerifyWebhook(_ context.Context, rawBody []byte, headers http.Header) (provider.VerifiedEvent, error) {
	if p.keyErr != nil || p.platformKey == nil || len(p.apiV3Key) != apiV3KeyLen {
		// Fail-closed: a missing/garbage verify key or APIv3Key rejects everything.
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	sigB64 := headers.Get("Wechatpay-Signature")
	timestamp := headers.Get("Wechatpay-Timestamp")
	nonce := headers.Get("Wechatpay-Nonce")
	serial := headers.Get("Wechatpay-Serial")
	if sigB64 == "" || timestamp == "" || nonce == "" || serial == "" {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Select the platform public key by Wechatpay-Serial (platform-cert rotation).
	// An unknown/mismatched serial → reject (fail-closed; do NOT skip verification).
	if serial != p.platSerial {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Replay layer 1 — Wechatpay-Timestamp freshness: reject a stale (or far-future)
	// callback BEFORE any verify (BR-W-5).
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	age := p.now().Unix() - ts
	if age < 0 {
		age = -age
	}
	if age > int64(p.tolerance/time.Second) {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Reconstruct EXACTLY: `<timestamp>\n<nonce>\n<rawBody>\n` (NO method/URI → no
	// r.URL.Path trap, BR-W-2).
	signStr := callbackSignString(timestamp, nonce, rawBody)
	digest := sha256.Sum256([]byte(signStr))
	if err := rsa.VerifyPKCS1v15(p.platformKey, crypto.SHA256, digest[:], sig); err != nil {
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	// Signature OK — NOW (and only now) decrypt the resource (no decrypt oracle).
	return p.decryptAndParse(rawBody)
}

// decryptAndParse AES-256-GCM-decrypts the verified callback's resource and
// normalises it. A decrypt/tag failure is treated as a signature failure
// (fail-closed, NEVER a partial-plaintext parse, BR-W-3).
func (p *Provider) decryptAndParse(rawBody []byte) (provider.VerifiedEvent, error) {
	var env callbackEnvelope
	if err := json.Unmarshal(rawBody, &env); err != nil {
		// Signature already verified — an unparseable envelope is a contract error.
		return provider.VerifiedEvent{}, fmt.Errorf("wechat: parse callback envelope: %w", err)
	}
	plain, err := p.decryptResource(env.Resource)
	if err != nil {
		// Tampered ciphertext / wrong APIv3Key → GCM tag mismatch → fail-closed.
		// Treated as a signature-class failure so the handler 400s and NEVER credits.
		return provider.VerifiedEvent{}, provider.ErrSignatureInvalid
	}
	return parseTransaction(env.EventType, plain)
}

// decryptResource AES-256-GCM-decrypts an APIv3 resource. The 16-byte GCM auth tag
// (trailing the ciphertext) is consumed by gcm.Open and is the integrity guard:
// any tampering OR a wrong key → a non-nil error (BR-W-3).
func (p *Provider) decryptResource(r resource) ([]byte, error) {
	if !strings.EqualFold(r.Algorithm, "AEAD_AES_256_GCM") {
		return nil, fmt.Errorf("wechat: unsupported resource algorithm")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(r.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("wechat: bad resource ciphertext")
	}
	block, err := aes.NewCipher(p.apiV3Key)
	if err != nil {
		return nil, fmt.Errorf("wechat: cipher init")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("wechat: gcm init")
	}
	nonce := []byte(r.Nonce)
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("wechat: bad resource nonce")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(r.AssociatedData))
	if err != nil {
		// Tag mismatch (tampered ciphertext/nonce/AAD or wrong APIv3Key) — fail-closed.
		return nil, fmt.Errorf("wechat: resource decrypt failed")
	}
	return plain, nil
}

// callbackEnvelope is the minimal outer (signed, plaintext) callback body.
type callbackEnvelope struct {
	ID           string   `json:"id"`
	EventType    string   `json:"event_type"`
	ResourceType string   `json:"resource_type"`
	Resource     resource `json:"resource"`
}

// resource is the AES-256-GCM-encrypted inner payload (the FIRST encrypted body on
// the platform, Q-ENCRYPT).
type resource struct {
	Algorithm      string `json:"algorithm"`
	Ciphertext     string `json:"ciphertext"`
	Nonce          string `json:"nonce"`
	AssociatedData string `json:"associated_data"`
	OriginalType   string `json:"original_type"`
}

// transaction is the decrypted (plaintext) WeChat payment transaction.
type transaction struct {
	OutTradeNo    string `json:"out_trade_no"`
	TransactionID string `json:"transaction_id"`
	TradeState    string `json:"trade_state"`
	Amount        struct {
		Total    int64  `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

// parseTransaction normalises a verified+decrypted WeChat transaction into a
// VerifiedEvent. SettledAmount is the PROVIDER-CONFIRMED settled value (minor-unit
// →string-decimal), never the create-order intent (BR-C-3). trade_state gates the
// credit (Q-CONFIRM): SUCCESS→paid; NOTPAY/USERPAYING→EventUnhandled (200-ACK
// no-op); CLOSED/PAYERROR/REVOKED→failed; a REFUND.* event_type→EventUnhandled.
func parseTransaction(eventType string, plain []byte) (provider.VerifiedEvent, error) {
	// A REFUND.* callback carries a refund resource, not a transaction — 200-ACK
	// no-op (Q-REFUND deferred), no money action.
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(eventType)), "REFUND") {
		return provider.VerifiedEvent{Provider: "wechat", Kind: provider.EventUnhandled}, nil
	}
	var tx transaction
	if err := json.Unmarshal(plain, &tx); err != nil {
		// Decrypt already succeeded (tag verified) — an unparseable plaintext is a
		// contract error; surface it so the handler 400s without crediting.
		return provider.VerifiedEvent{}, fmt.Errorf("wechat: parse transaction: %w", err)
	}
	ev := provider.VerifiedEvent{
		Provider:        "wechat",
		OrderID:         tx.OutTradeNo, // OUR order id — never a client user_id (BR-A-3)
		ExternalOrderID: tx.TransactionID,
		Currency:        strings.ToUpper(strings.TrimSpace(tx.Amount.Currency)),
	}
	switch strings.ToUpper(strings.TrimSpace(tx.TradeState)) {
	case "SUCCESS":
		// WeChat finality → credit. Convert the minor-unit total to a string-decimal;
		// a scale failure fails CLOSED (no mis-scaled 100× credit, BR-W-8).
		settled, err := minorIntToDecimal(tx.Amount.Total, tx.Amount.Currency)
		if err != nil {
			return provider.VerifiedEvent{}, fmt.Errorf("wechat: settled amount: %w", err)
		}
		ev.SettledAmount = settled
		ev.Kind = provider.EventRechargePaid
		ev.Status = "paid"
	case "CLOSED", "PAYERROR", "REVOKED":
		ev.Kind = provider.EventRechargeFailed
		ev.Status = "failed"
	default:
		// NOTPAY / USERPAYING (in-progress) or any unknown state → 200-ACK no-op, NO
		// credit (crediting a non-terminal result risks crediting a payment that
		// never completes, Q-CONFIRM / BR-C-1).
		ev.Kind = provider.EventUnhandled
	}
	return ev, nil
}

// callbackSignString builds the WeChat APIv3 callback signed string:
// `<timestamp>\n<nonce>\n<body>\n` (NOTE the trailing newline; NO method/URI).
func callbackSignString(timestamp, nonce string, body []byte) string {
	var b strings.Builder
	b.Grow(len(timestamp) + len(nonce) + len(body) + 3)
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.WriteString(nonce)
	b.WriteByte('\n')
	b.Write(body)
	b.WriteByte('\n')
	return b.String()
}

// requestSignString builds the WeChat APIv3 OUTBOUND signed string:
// `<METHOD>\n<URL>\n<timestamp>\n<nonce>\n<body>\n`.
func requestSignString(method, urlPath, timestamp, nonce string, body []byte) string {
	var b strings.Builder
	b.Grow(len(method) + len(urlPath) + len(timestamp) + len(nonce) + len(body) + 5)
	b.WriteString(method)
	b.WriteByte('\n')
	b.WriteString(urlPath)
	b.WriteByte('\n')
	b.WriteString(timestamp)
	b.WriteByte('\n')
	b.WriteString(nonce)
	b.WriteByte('\n')
	b.Write(body)
	b.WriteByte('\n')
	return b.String()
}

// decimalToMinorInt converts a string-decimal amount to a WeChat integer minor-unit
// value for currency (Q-AMOUNT-UNITS). Fails CLOSED on sub-minor precision. "50.00"
// /USD → 5000.
func decimalToMinorInt(amount, currency string) (int64, error) {
	s, err := decimalToMinor(amount, currency)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("wechat: minor-unit overflow")
	}
	return v, nil
}

// decimalToMinor converts a string-decimal amount to an integer minor-unit STRING
// for currency (Q-AMOUNT-UNITS). Fails CLOSED on sub-minor precision (a scale loss
// would mis-scale the charge). "50.00"/USD → "5000".
func decimalToMinor(amount, currency string) (string, error) {
	digits, ok := minorUnitDigits[strings.ToUpper(strings.TrimSpace(currency))]
	if !ok {
		return "", fmt.Errorf("wechat: unsupported currency %q", currency)
	}
	d, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil {
		return "", fmt.Errorf("wechat: bad amount %q", amount)
	}
	scaled := d.Shift(digits) // ×10^digits
	if !scaled.Equal(scaled.Truncate(0)) {
		// Sub-minor precision (e.g. "50.001" USD) → reject rather than silently lose it.
		return "", fmt.Errorf("wechat: amount %q has sub-minor precision for %s", amount, currency)
	}
	return scaled.Truncate(0).String(), nil
}

// minorIntToDecimal converts a WeChat integer minor-unit value to a string-decimal
// for currency (Q-AMOUNT-UNITS). "5000"/USD → "50.00".
func minorIntToDecimal(value int64, currency string) (string, error) {
	return minorToDecimal(strconv.FormatInt(value, 10), currency)
}

// minorToDecimal converts an integer minor-unit STRING to a string-decimal for
// currency (Q-AMOUNT-UNITS). Fails CLOSED on a non-integer minor value. "5000"/USD
// → "50.00".
func minorToDecimal(value, currency string) (string, error) {
	digits, ok := minorUnitDigits[strings.ToUpper(strings.TrimSpace(currency))]
	if !ok {
		return "", fmt.Errorf("wechat: unsupported currency %q", currency)
	}
	v, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("wechat: bad minor-unit value %q", value)
	}
	if !v.Equal(v.Truncate(0)) {
		return "", fmt.Errorf("wechat: minor-unit value %q is not an integer", value)
	}
	return v.Shift(-digits).StringFixed(digits), nil
}

// postSigned issues a JSON WeChat APIv3 REST call signed with our merchant private
// key over the `<METHOD>\n<URL>\n<timestamp>\n<nonce>\n<body>\n` shape. The surfaced
// error NEVER embeds the key / url / body (BR-W-7).
func (p *Provider) postSigned(ctx context.Context, path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("wechat: marshal request")
	}
	timestamp := strconv.FormatInt(p.now().Unix(), 10)
	nonce, err := randomNonce()
	if err != nil {
		return fmt.Errorf("wechat: nonce")
	}
	signStr := requestSignString(http.MethodPost, path, timestamp, nonce, buf)
	digest := sha256.Sum256([]byte(signStr))
	raw, err := rsa.SignPKCS1v15(rand.Reader, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return fmt.Errorf("wechat: sign request")
	}
	authz := fmt.Sprintf(`%s mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`,
		authSchema, p.mchID, nonce, timestamp, p.certSerial, base64.StdEncoding.EncodeToString(raw))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("wechat: build request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authz)
	resp, err := p.client.Do(req)
	if err != nil {
		// NEVER embed the secret/URL in the surfaced error.
		return fmt.Errorf("wechat: request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wechat: provider returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("wechat: decode response")
	}
	return nil
}

// randomNonce returns a 32-char hex nonce for the outbound Authorization header.
func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// parsePrivateKey parses an RSA private key from a PEM block (PKCS#8 or PKCS#1) or
// a raw base64 DER body. Returns a generic error (no key material) on failure.
func parsePrivateKey(s string) (*rsa.PrivateKey, error) {
	der, err := pemOrBase64(s)
	if err != nil {
		return nil, fmt.Errorf("wechat: parse private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
	}
	return nil, fmt.Errorf("wechat: unsupported private key format")
}

// parsePublicKey parses an RSA public key from a PEM block (PKIX or PKCS#1), an
// x509 certificate PEM (WeChat distributes the platform key as a certificate), or a
// raw base64 DER body. Returns a generic error on failure.
func parsePublicKey(s string) (*rsa.PublicKey, error) {
	der, err := pemOrBase64(s)
	if err != nil {
		return nil, fmt.Errorf("wechat: parse public key")
	}
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
	}
	if k, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return k, nil
	}
	// WeChat distributes the platform key as an x509 certificate — extract its key.
	if cert, err := x509.ParseCertificate(der); err == nil {
		if rk, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return rk, nil
		}
	}
	return nil, fmt.Errorf("wechat: unsupported public key format")
}

// pemOrBase64 returns the DER bytes from a PEM block if present, else base64-decodes
// the trimmed string.
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
