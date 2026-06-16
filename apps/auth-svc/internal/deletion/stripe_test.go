package deletion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// INT-022 (adapter) — detach issues POST /v1/payment_methods/{id}/detach with
// bearer auth; 2xx → success.
func TestStripeAdapter_DetachSuccess(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"pm_x","object":"payment_method"}`))
	}))
	defer srv.Close()

	a := NewStripeAdapter("sk_test_123", srv.URL)
	if err := a.DetachPaymentMethod(context.Background(), "pm_live_abc"); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/v1/payment_methods/pm_live_abc/detach" {
		t.Errorf("method/path = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer sk_test_123" {
		t.Errorf("auth = %q", gotAuth)
	}
}

// BLIND-ERROR-003 (adapter) — a 5xx is an error so the reconcile sweep retries.
func TestStripeAdapter_DetachServerErrorRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	a := NewStripeAdapter("sk", srv.URL)
	err := a.DetachPaymentMethod(context.Background(), "pm_x")
	if err == nil {
		t.Fatal("want error on 503")
	}
	if strings.Contains(err.Error(), "sk") {
		t.Error("error leaked the secret key")
	}
}

// Idempotency — a 404 (already detached) is treated as success so reconcile converges.
func TestStripeAdapter_DetachNotFoundIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	a := NewStripeAdapter("sk", srv.URL)
	if err := a.DetachPaymentMethod(context.Background(), "pm_gone"); err != nil {
		t.Errorf("404 should be idempotent success, got %v", err)
	}
}
