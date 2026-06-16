package deletion

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// stripeDefaultBaseURL is the Stripe REST base. Overridable for tests / sandbox.
const stripeDefaultBaseURL = "https://api.stripe.com"

// StripeAdapter implements StripeDetacher against the Stripe REST API using raw
// net/http + bearer auth (mirrors apps/payment-svc/internal/provider/stripe —
// no third-party SDK). The secret key is NEVER logged or embedded in errors.
type StripeAdapter struct {
	secretKey string
	baseURL   string
	client    *http.Client
}

// NewStripeAdapter builds the detach client. baseURL defaults to the live Stripe
// API when empty.
func NewStripeAdapter(secretKey, baseURL string) *StripeAdapter {
	if baseURL == "" {
		baseURL = stripeDefaultBaseURL
	}
	return &StripeAdapter{
		secretKey: secretKey,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

// DetachPaymentMethod detaches a live off-session PaymentMethod (token = pm_…)
// from Stripe (OQ-3 OVERRIDE / M-2). Idempotent: a 404 (the PaymentMethod no
// longer exists — already detached on a prior run) is treated as success so the
// reconcile sweep converges. Any other non-2xx is an error → retried next run.
func (a *StripeAdapter) DetachPaymentMethod(ctx context.Context, token string) error {
	endpoint := a.baseURL + "/v1/payment_methods/" + url.PathEscape(token) + "/detach"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(""))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+a.secretKey)

	resp, err := a.client.Do(req)
	if err != nil {
		return err // transport error — reconcile retries
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// Already detached / no such PaymentMethod — idempotent success.
		return nil
	default:
		// NEVER embed the secret/URL in the error (coding-standards §12.3).
		return fmt.Errorf("stripe detach: unexpected status %d", resp.StatusCode)
	}
}

// ensure interface satisfaction at compile time.
var _ StripeDetacher = (*StripeAdapter)(nil)
