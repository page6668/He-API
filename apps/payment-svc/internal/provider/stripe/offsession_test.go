// Story 7.7 AC1 — Stripe off-session charge + SetupIntent save-token tests
// (7.7-UNIT-030 shaping + metadata, 7.7-INT-031 decline→failed, 7.7-INT-032
// 3DS→failed, SetupIntent client_secret, RetrievePaymentMethod display-safe).
// All run offline against an httptest-mocked Stripe ([[project_toolchain_env_limits]]).
package stripe

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// 7.7-UNIT-030 — ChargeOffSession shapes an off_session PaymentIntent confirm with
// the stored token + correct minor-units + OUR order id metadata; accepted → pending.
func TestChargeOffSession_ShapesAndPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payment_intents" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		for _, want := range []string{"amount=2000", "off_session=true", "confirm=true", "payment_method=pm_tok", "he_order_id"} {
			if !contains(s, want) {
				t.Errorf("form missing %q: %s", want, s)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pi_off_1","status":"succeeded"}`))
	}))
	defer srv.Close()

	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	ext, status, err := p.ChargeOffSession(context.Background(), "order-1", "pm_tok", "20.00")
	if err != nil {
		t.Fatalf("ChargeOffSession: %v", err)
	}
	if ext != "pi_off_1" || status != "pending" {
		t.Fatalf("ext=%q status=%q, want pi_off_1/pending", ext, status)
	}
}

// 7.7-INT-031 — a 402 card decline maps to a CLEAN "failed" status (not a transport
// error); the trigger marks the order failed + fires the alert.
func TestChargeOffSession_DeclinedIsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"error":{"code":"card_declined"},"payment_intent":{"id":"pi_dead"}}`))
	}))
	defer srv.Close()
	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	_, status, err := p.ChargeOffSession(context.Background(), "order-1", "pm_dead", "20.00")
	if err != nil {
		t.Fatalf("a decline must not be a transport error: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
}

// 7.7-INT-032 — off_session cannot do interactive auth: a requires_action status →
// treated as failed.
func TestChargeOffSession_RequiresActionIsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pi_3ds","status":"requires_action"}`))
	}))
	defer srv.Close()
	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	_, status, _ := p.ChargeOffSession(context.Background(), "order-1", "pm_x", "20.00")
	if status != "failed" {
		t.Fatalf("requires_action status = %q, want failed", status)
	}
}

// CreateSetupIntent returns the client_secret + id (PAN never touches He-API).
func TestCreateSetupIntent_ReturnsClientSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/setup_intents" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !contains(string(body), "usage=off_session") {
			t.Errorf("SetupIntent not off_session: %s", body)
		}
		_, _ = w.Write([]byte(`{"id":"seti_1","client_secret":"seti_1_secret_xyz"}`))
	}))
	defer srv.Close()
	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	secret, id, err := p.CreateSetupIntent(context.Background(), "u1")
	if err != nil {
		t.Fatalf("CreateSetupIntent: %v", err)
	}
	if secret != "seti_1_secret_xyz" || id != "seti_1" {
		t.Fatalf("secret=%q id=%q", secret, id)
	}
}

// RetrievePaymentMethod returns ONLY the token + display-safe brand/last4 (no PAN).
func TestRetrievePaymentMethod_DisplaySafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"seti_1","payment_method":{"id":"pm_saved","card":{"brand":"visa","last4":"4242"}}}`))
	}))
	defer srv.Close()
	p := New("sk_test", testSecret, WithBaseURL(srv.URL))
	tok, brand, last4, err := p.RetrievePaymentMethod(context.Background(), "seti_1")
	if err != nil {
		t.Fatalf("RetrievePaymentMethod: %v", err)
	}
	if tok != "pm_saved" || brand != "visa" || last4 != "4242" {
		t.Fatalf("tok=%q brand=%q last4=%q", tok, brand, last4)
	}
}

// 7.7-UNIT-012 — token discipline: a provider error never embeds the secret key or
// the stored token (the surfaced error is generic).
func TestChargeOffSession_ErrorNeverLeaksSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := New("sk_test_SECRET", testSecret, WithBaseURL(srv.URL))
	_, _, err := p.ChargeOffSession(context.Background(), "order-1", "pm_SENSITIVE", "20.00")
	if err == nil {
		t.Fatal("expected an error on 500")
	}
	if contains(err.Error(), "sk_test_SECRET") || contains(err.Error(), "pm_SENSITIVE") {
		t.Fatalf("error leaked a secret/token: %v", err)
	}
}

// 7.7-UNIT-031 (handler-level scope note): the off-session capability is opt-in.
// A provider that does not implement OffSessionProvider yields ErrOffSessionUnsupported
// at the handler; here we assert the Stripe impl DOES satisfy the interface.
func TestStripe_SatisfiesOffSessionProvider(t *testing.T) {
	var _ provider.OffSessionProvider = New("sk_test", testSecret)
}
