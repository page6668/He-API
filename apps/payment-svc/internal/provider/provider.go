// Package provider defines the PaymentProvider seam — the pluggable payment-
// channel abstraction Story 7.3 stands up and Stories 7.4 (USDC) / 7.5 (Alipay+)
// / 7.6 (WeChat Pay HK) extend WITHOUT touching the seam or the webhook/recharge
// handlers (Q-PROVIDER-SEAM, the single most valuable forward-compat decision in
// the story). It mirrors the 7.2 FxProvider seam: a small interface + an
// env-driven registry, with concrete impls in sub-packages (stripe/, paypal/).
//
// Three capabilities:
//   - CreateCheckout      — open a provider-hosted checkout for a pending order
//     (PCI §8.4: card data goes browser→provider, never to He-API).
//   - CreateSubscription  — create a provider subscription (RAIL only, Q-SUBSCOPE).
//   - VerifyWebhook       — verify a provider webhook signature against the EXACT
//     raw bytes (the signature IS the credential — the route is otherwise
//     unauthenticated, BR-W-1) and normalise it to a VerifiedEvent.
package provider

import (
	"context"
	"errors"
	"net/http"
)

// Order is a pending recharge order handed to CreateCheckout. Amount is the
// string-decimal INTENT (display/UX) — the credited value is the provider-
// confirmed settled amount from the verified webhook (Q-AMOUNT).
type Order struct {
	OrderID  string // our recharge_orders.id (carried into the provider as metadata)
	UserID   string
	Amount   string // string-decimal
	Currency string
}

// CheckoutResult is the provider-hosted checkout handle.
type CheckoutResult struct {
	CheckoutURL     string // provider-hosted redirect (Stripe Checkout / PayPal approve)
	ClientToken     string // alternative to a redirect URL (provider-dependent)
	ExternalOrderID string // provider session/order/intent id
}

// Subscription is a subscription create request (RAIL only — plan is opaque in
// 7.3; the tier→entitlement mapping is Story 7.8, Q-SUBSCOPE).
type Subscription struct {
	SubscriptionID string // our subscriptions.id
	UserID         string
	Plan           string // opaque ('free'|'pro'|'team'|'enterprise')
}

// SubscriptionResult is the provider subscription handle.
type SubscriptionResult struct {
	CheckoutURL            string
	ExternalSubscriptionID string
}

// EventKind classifies a verified webhook so the producer/consumer can route it
// without re-parsing provider-specific payloads. UNHANDLED is the safe default
// for events 7.3 acknowledges-but-no-ops (refund/dispute, Q-REFUND).
type EventKind string

const (
	EventRechargePaid        EventKind = "recharge_paid"
	EventRechargeFailed      EventKind = "recharge_failed"
	EventSubscriptionActive  EventKind = "subscription_active"
	EventSubscriptionRenew   EventKind = "subscription_renew"
	EventSubscriptionPastDue EventKind = "subscription_past_due"
	EventSubscriptionCancel  EventKind = "subscription_cancel"
	// EventUnhandled — a verified event 7.3 does not act on (refund/dispute,
	// unknown type). The webhook handler 200-ACKs it without emitting (Q-REFUND).
	EventUnhandled EventKind = "unhandled"
)

// VerifiedEvent is the provider-agnostic normalisation of a verified webhook.
// SettledAmount is the PROVIDER TRUTH (string-decimal) — never a client value.
type VerifiedEvent struct {
	Provider               string
	Kind                   EventKind
	OrderID                string // our order id, resolved from provider metadata (NEVER a client-asserted user_id — BR-R-4)
	ExternalOrderID        string // provider charge/intent/session id
	ExternalSubscriptionID string // provider subscription id (subscription events)
	SettledAmount          string // string-decimal; the credited value (Q-AMOUNT)
	Currency               string
	Status                 string // paid | failed | cancelled | past_due
}

// PaymentProvider is the seam. Implementations live in sub-packages and are
// wired by the Registry. Stripe + PayPal in 7.3; 7.4-7.6 add impls here.
type PaymentProvider interface {
	// Name is the canonical provider id (stripe | paypal | ...).
	Name() string
	// CreateCheckout opens a provider-hosted checkout for a pending order.
	CreateCheckout(ctx context.Context, o Order) (CheckoutResult, error)
	// CreateSubscription creates a provider subscription (RAIL only).
	CreateSubscription(ctx context.Context, s Subscription) (SubscriptionResult, error)
	// VerifyWebhook verifies the signature over the EXACT raw bytes and normalises
	// the event. It MUST return ErrSignatureInvalid (and parse nothing into a
	// money action) on any signature failure (BR-W-1).
	VerifyWebhook(ctx context.Context, rawBody []byte, headers http.Header) (VerifiedEvent, error)
}

// ErrSignatureInvalid is returned by VerifyWebhook when the provider signature
// is absent, malformed, forged, or stale (replay). The webhook handler maps it
// to 400_webhook_signature_invalid and never parses the body (BR-W-1 / BR-W-4).
var ErrSignatureInvalid = errors.New("provider: webhook signature verification failed")

// OffSessionProvider is the OPTIONAL Story-7.7 extension of the seam (Q-OFFSESSION):
// merchant-initiated (off-session) charge against a STORED token + the SetupIntent
// save-token flow. Only the card-capable Stripe impl satisfies it; the payment
// handler type-asserts it and returns ErrOffSessionUnsupported for any provider
// that does not — so the 7.4-7.6 impls (USDC / Alipay+ / WeChat) stay UNTOUCHED
// (boundary lock). The off-session charge still settles via the 7.3 webhook → the
// exactly-once credit path is UNCHANGED (no new credit code).
type OffSessionProvider interface {
	// ChargeOffSession confirms a charge against pmToken with no user present. It
	// carries orderID into the provider as metadata so the 7.3 webhook resolves
	// OUR order. amountUSD is a string-decimal. status is "pending" (accepted →
	// webhook will settle) or "failed" (declined / requires interactive auth).
	ChargeOffSession(ctx context.Context, orderID, pmToken, amountUSD string) (extOrderID, status string, err error)
	// CreateSetupIntent begins a SetupIntent and returns its client_secret (for
	// client-side card confirmation — the PAN never reaches He-API, PCI §8.4).
	CreateSetupIntent(ctx context.Context, userID string) (clientSecret, setupIntentID string, err error)
	// RetrievePaymentMethod fetches the confirmed token + display-safe brand/last4
	// off a completed SetupIntent (server-side — the token is never client-asserted).
	RetrievePaymentMethod(ctx context.Context, setupIntentID string) (pmToken, brand, last4 string, err error)
}

// ErrOffSessionUnsupported is returned for a provider that does not implement
// OffSessionProvider (Q-OFFSESSION: card off-session is Stripe-only in 7.7).
var ErrOffSessionUnsupported = errors.New("provider: off-session charge unsupported")

// ErrUnsupportedProvider is returned by the Registry for an unknown provider id.
var ErrUnsupportedProvider = errors.New("provider: unsupported payment provider")

// Registry resolves a provider id to its PaymentProvider impl. Mirrors the 7.2
// fx ProviderFromEnv selection — built once at boot, read-only thereafter.
type Registry struct {
	providers map[string]PaymentProvider
}

// NewRegistry builds a Registry from the given impls (nil entries are skipped).
func NewRegistry(impls ...PaymentProvider) *Registry {
	m := make(map[string]PaymentProvider, len(impls))
	for _, p := range impls {
		if p == nil {
			continue
		}
		m[p.Name()] = p
	}
	return &Registry{providers: m}
}

// Get returns the impl for a provider id, or ErrUnsupportedProvider.
func (r *Registry) Get(name string) (PaymentProvider, error) {
	if r == nil {
		return nil, ErrUnsupportedProvider
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, ErrUnsupportedProvider
	}
	return p, nil
}

// Has reports whether a provider id is wired.
func (r *Registry) Has(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.providers[name]
	return ok
}

// Names returns the wired provider ids (unordered) — for boot logging.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.providers))
	for n := range r.providers {
		out = append(out, n)
	}
	return out
}
