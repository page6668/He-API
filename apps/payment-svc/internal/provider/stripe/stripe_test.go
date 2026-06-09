// Story 7.3 adversarial signature lanes for the Stripe provider (7.3-UNIT-005/006/007).
// The webhook signature IS the credential — these prove a genuine signature
// verifies + parses, while forged / absent / tampered / stale signatures are
// rejected with provider.ErrSignatureInvalid and NOTHING is parsed into a money
// action (BR-W-1 / BR-W-4).
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

const testSecret = "whsec_test_secret"

var fixedNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// sign builds a valid Stripe-Signature header for a payload at timestamp ts.
func sign(secret string, ts int64, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func newProvider() *Provider {
	return New("sk_test", testSecret, WithClock(func() time.Time { return fixedNow }))
}

const rechargePaidBody = `{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","payment_intent":"pi_123","currency":"usd","amount_total":5000,"metadata":{"he_order_id":"order-abc"}}}}`

func TestVerifyWebhook_GenuineSignature_Parses(t *testing.T) {
	p := newProvider()
	hdr := http.Header{}
	hdr.Set("Stripe-Signature", sign(testSecret, fixedNow.Unix(), []byte(rechargePaidBody)))

	ev, err := p.VerifyWebhook(context.Background(), []byte(rechargePaidBody), hdr)
	if err != nil {
		t.Fatalf("genuine signature rejected: %v", err)
	}
	if ev.Kind != provider.EventRechargePaid {
		t.Errorf("Kind = %q, want recharge_paid", ev.Kind)
	}
	if ev.OrderID != "order-abc" {
		t.Errorf("OrderID = %q, want order-abc (from metadata, BR-R-4)", ev.OrderID)
	}
	if ev.SettledAmount != "50.00" {
		t.Errorf("SettledAmount = %q, want 50.00 (cents→decimal)", ev.SettledAmount)
	}
	if ev.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", ev.Currency)
	}
	if ev.ExternalOrderID != "pi_123" {
		t.Errorf("ExternalOrderID = %q, want pi_123", ev.ExternalOrderID)
	}
}

func TestVerifyWebhook_AbsentSignature_Rejected(t *testing.T) {
	p := newProvider()
	_, err := p.VerifyWebhook(context.Background(), []byte(rechargePaidBody), http.Header{})
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("absent signature: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestVerifyWebhook_ForgedSignature_Rejected(t *testing.T) {
	p := newProvider()
	hdr := http.Header{}
	hdr.Set("Stripe-Signature", "t="+strconv.FormatInt(fixedNow.Unix(), 10)+",v1=deadbeef")
	_, err := p.VerifyWebhook(context.Background(), []byte(rechargePaidBody), hdr)
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("forged signature: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestVerifyWebhook_TamperedBody_Rejected(t *testing.T) {
	p := newProvider()
	// Sign the genuine body, then deliver a body with the amount inflated.
	hdr := http.Header{}
	hdr.Set("Stripe-Signature", sign(testSecret, fixedNow.Unix(), []byte(rechargePaidBody)))
	tampered := `{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","amount_total":9999999,"metadata":{"he_order_id":"order-abc"}}}}`
	_, err := p.VerifyWebhook(context.Background(), []byte(tampered), hdr)
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("tampered body: err = %v, want ErrSignatureInvalid (HMAC over exact bytes)", err)
	}
}

func TestVerifyWebhook_StaleTimestamp_Rejected(t *testing.T) {
	p := newProvider()
	staleTs := fixedNow.Add(-10 * time.Minute).Unix() // outside 5m tolerance
	hdr := http.Header{}
	hdr.Set("Stripe-Signature", sign(testSecret, staleTs, []byte(rechargePaidBody)))
	_, err := p.VerifyWebhook(context.Background(), []byte(rechargePaidBody), hdr)
	if !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("stale signature (replay): err = %v, want ErrSignatureInvalid", err)
	}
}

func TestParseEvent_SubscriptionRenew(t *testing.T) {
	body := `{"type":"invoice.paid","data":{"object":{"id":"in_1","subscription":"sub_123","amount_paid":2000,"currency":"usd","metadata":{"he_order_id":"sub-row-1"}}}}`
	ev, err := parseEvent([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Kind != provider.EventSubscriptionRenew {
		t.Errorf("Kind = %q, want subscription_renew", ev.Kind)
	}
	if ev.ExternalSubscriptionID != "sub_123" {
		t.Errorf("ExternalSubscriptionID = %q, want sub_123", ev.ExternalSubscriptionID)
	}
}

func TestParseEvent_RefundUnhandled(t *testing.T) {
	// Q-REFUND: refund/dispute events are verified but no-op (EventUnhandled).
	body := `{"type":"charge.refunded","data":{"object":{"id":"ch_1"}}}`
	ev, err := parseEvent([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Kind != provider.EventUnhandled {
		t.Errorf("Kind = %q, want unhandled (refund 200-ACK no-op)", ev.Kind)
	}
}

func TestCreateCheckout_PostsAndReturnsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/checkout/sessions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "order-abc" {
			t.Errorf("missing/incorrect Idempotency-Key: %q (BR-R-6)", r.Header.Get("Idempotency-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		if !contains(string(body), "he_order_id") {
			t.Errorf("order id not carried as metadata: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_test_123","url":"https://checkout.stripe.com/c/pay/cs_test_123","payment_intent":"pi_123"}`))
	}))
	defer srv.Close()

	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	res, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "order-abc", UserID: "u1", Amount: "50.00", Currency: "USD"})
	if err != nil {
		t.Fatalf("CreateCheckout: %v", err)
	}
	if res.CheckoutURL == "" {
		t.Errorf("empty CheckoutURL")
	}
	if res.ExternalOrderID != "pi_123" {
		t.Errorf("ExternalOrderID = %q, want pi_123", res.ExternalOrderID)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
