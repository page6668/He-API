// Story 7.3 (AC3) — 7.3-INT-012: the gateway forwards the webhook body
// BYTE-FOR-BYTE to payment-svc (raw-body integrity for HMAC, BR-W-2). The proxy
// must NOT deserialize/normalize the body, and must relay the provider signature
// header + the downstream status code verbatim (BR-W-5).
package handlers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebhookProxy_ForwardsRawBytesAndHeader(t *testing.T) {
	const rawBody = `{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"amount_total":5000}}}`
	const sig = "t=123,v1=abcdef"

	var gotBody []byte
	var gotSig string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSig = r.Header.Get("Stripe-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	h := NewWebhookProxyHandler(nil, upstream.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/stripe", bytes.NewReader([]byte(rawBody)))
	req.Header.Set("Stripe-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Handle("stripe")(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (relayed)", rec.Code)
	}
	if gotPath != "/webhooks/stripe" {
		t.Errorf("upstream path = %q, want /webhooks/stripe", gotPath)
	}
	if string(gotBody) != rawBody {
		t.Errorf("body not byte-faithful:\n got=%q\nwant=%q", gotBody, rawBody)
	}
	if gotSig != sig {
		t.Errorf("signature header not forwarded: %q", gotSig)
	}
}

func TestWebhookProxy_RelaysRejectStatus(t *testing.T) {
	// A forged webhook: payment-svc returns 400; the gateway relays it verbatim so
	// the provider sees the rejection (it never reaches a money action).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer upstream.Close()

	h := NewWebhookProxyHandler(nil, upstream.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/stripe", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.Handle("stripe")(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (relayed forgery rejection)", rec.Code)
	}
}

func TestWebhookProxy_Unconfigured_503(t *testing.T) {
	h := NewWebhookProxyHandler(nil, "", nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhooks/stripe", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.Handle("stripe")(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
