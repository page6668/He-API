// Story 7.3 PayPal provider parity lanes (7.3-UNIT-008, 7.3-INT-005). The PayPal
// transmission signature is verified via the provider's verify-webhook-signature
// endpoint: a SUCCESS verdict parses the event; a FAILURE verdict (or missing
// transmission headers) is rejected with provider.ErrSignatureInvalid, parity
// with the Stripe lane (BR-W-1).
package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

const capturedBody = `{"id":"WH-1","event_type":"PAYMENT.CAPTURE.COMPLETED","resource":{"id":"CAP-1","custom_id":"order-xyz","amount":{"value":"75.00","currency_code":"USD"}}}`

// stubServer returns a PayPal API stub: /v1/oauth2/token issues a token;
// the verify endpoint returns `verdict`.
func stubServer(t *testing.T, verdict string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"A123","token_type":"Bearer"}`))
		case "/v1/notifications/verify-webhook-signature":
			var got map[string]any
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got["webhook_id"] != "wh_id_1" {
				t.Errorf("verify request missing webhook_id binding: %v", got["webhook_id"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"verification_status":"` + verdict + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func verifyHeaders() http.Header {
	h := http.Header{}
	h.Set("Paypal-Auth-Algo", "SHA256withRSA")
	h.Set("Paypal-Cert-Url", "https://api.paypal.com/cert.pem")
	h.Set("Paypal-Transmission-Id", "tx-1")
	h.Set("Paypal-Transmission-Sig", "c2ln")
	h.Set("Paypal-Transmission-Time", "2026-06-09T12:00:00Z")
	return h
}

func TestVerifyWebhook_Success_Parses(t *testing.T) {
	srv := stubServer(t, "SUCCESS")
	defer srv.Close()
	p := New("cid", "secret", "wh_id_1", WithBaseURL(srv.URL))

	ev, err := p.VerifyWebhook(context.Background(), []byte(capturedBody), verifyHeaders())
	if err != nil {
		t.Fatalf("SUCCESS verdict rejected: %v", err)
	}
	if ev.Kind != provider.EventRechargePaid {
		t.Errorf("Kind = %q, want recharge_paid", ev.Kind)
	}
	if ev.OrderID != "order-xyz" {
		t.Errorf("OrderID = %q, want order-xyz (from custom_id, BR-R-4)", ev.OrderID)
	}
	if ev.SettledAmount != "75.00" || ev.Currency != "USD" {
		t.Errorf("amount/currency = %q/%q, want 75.00/USD", ev.SettledAmount, ev.Currency)
	}
	if ev.Provider != "paypal" {
		t.Errorf("Provider = %q, want paypal", ev.Provider)
	}
}

func TestVerifyWebhook_Failure_Rejected(t *testing.T) {
	srv := stubServer(t, "FAILURE")
	defer srv.Close()
	p := New("cid", "secret", "wh_id_1", WithBaseURL(srv.URL))

	_, err := p.VerifyWebhook(context.Background(), []byte(capturedBody), verifyHeaders())
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("FAILURE verdict: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestVerifyWebhook_MissingHeaders_Rejected(t *testing.T) {
	srv := stubServer(t, "SUCCESS")
	defer srv.Close()
	p := New("cid", "secret", "wh_id_1", WithBaseURL(srv.URL))

	// No transmission headers → reject before even calling the verify endpoint.
	_, err := p.VerifyWebhook(context.Background(), []byte(capturedBody), http.Header{})
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("missing headers: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestParseEvent_SubscriptionCancel(t *testing.T) {
	body := `{"event_type":"BILLING.SUBSCRIPTION.CANCELLED","resource":{"id":"I-SUB1","custom_id":"sub-row-1"}}`
	ev, err := parseEvent([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Kind != provider.EventSubscriptionCancel || ev.Status != "cancelled" {
		t.Errorf("Kind/Status = %q/%q, want subscription_cancel/cancelled", ev.Kind, ev.Status)
	}
}

func TestCreateCheckout_ReturnsApproveLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/oauth2/token":
			_, _ = w.Write([]byte(`{"access_token":"A123"}`))
		case "/v2/checkout/orders":
			if r.Header.Get("PayPal-Request-Id") != "order-xyz" {
				t.Errorf("missing idempotency key (BR-R-6): %q", r.Header.Get("PayPal-Request-Id"))
			}
			_, _ = w.Write([]byte(`{"id":"PP-ORDER-1","links":[{"rel":"approve","href":"https://www.paypal.com/checkoutnow?token=PP-ORDER-1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := New("cid", "secret", "wh_id_1", WithBaseURL(srv.URL))
	res, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "order-xyz", UserID: "u1", Amount: "75.00", Currency: "USD"})
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if res.ExternalOrderID != "PP-ORDER-1" || res.CheckoutURL == "" {
		t.Errorf("unexpected result: %+v", res)
	}
}
