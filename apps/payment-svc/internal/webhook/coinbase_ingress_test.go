// Story 7.4 — coinbase webhook ingress through the REAL Handler + the REAL
// coinbase.Provider (not a fakeProvider): this is the offline integration proof
// for the lanes the QA skeleton points here (7.4-INT-022 forged->400 + zero
// side-effect, 7.4-INT-024 2xx/4xx/5xx discipline, the confirm-credits-once and
// pending-no-credit boundaries at the producer boundary). The PG state-machine
// (replay/idempotency/atomicity) is exercised in billing-svc (reused from 7.3).
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/coinbase"
)

const cbWebhookSecret = "whsec_coinbase_ingress"

func cbSign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(cbWebhookSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func cbCharge(eventType, chargeID, orderID, settled string) []byte {
	b, _ := json.Marshal(map[string]any{"event": map[string]any{
		"id":   "evt-1",
		"type": eventType,
		"data": map[string]any{
			"id":       chargeID,
			"metadata": map[string]string{"order_id": orderID, "user_id": "u1"},
			"pricing":  map[string]any{"local": map[string]string{"amount": settled, "currency": "USD"}},
			"payments": []map[string]any{{"value": map[string]any{
				"local":  map[string]string{"amount": settled, "currency": "USD"},
				"crypto": map[string]string{"amount": settled, "currency": "USDC"},
			}}},
		},
	}})
	return b
}

func doSigned(h *Handler, body []byte, sig string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/coinbase", strings.NewReader(string(body)))
	if sig != "" {
		req.Header.Set("X-CC-Webhook-Signature", sig)
	}
	rec := httptest.NewRecorder()
	h.Handle("coinbase")(rec, req)
	return rec
}

func newCoinbaseHandler(em Emitter) *Handler {
	reg := provider.NewRegistry(coinbase.New("cc_api_key", cbWebhookSecret))
	return New(reg, em, nil)
}

// 7.4-INT-022 — a forged signature is rejected at the handler with 400 and NOTHING
// is emitted (zero downstream side effect → zero recharge_orders mutation / credit).
func TestCoinbaseIngress_Forged_Rejected(t *testing.T) {
	em := &fakeEmitter{}
	h := newCoinbaseHandler(em)
	body := cbCharge("charge:confirmed", "CH-1", "order-1", "50.00")

	rec := doSigned(h, body, "deadbeefdeadbeef") // wrong signature
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged: status = %d, want 400", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("forged: emitter called %d times, want 0 (body never trusted, BR-W-1)", em.callerCt)
	}
}

// 7.4-INT-024 (genuine lane) + confirm-credits-once boundary — a valid
// charge:confirmed verifies and emits exactly one recharge_paid PaymentEvent
// carrying the provider-confirmed settled amount + provider="coinbase".
func TestCoinbaseIngress_Confirmed_EmitsOnce(t *testing.T) {
	em := &fakeEmitter{}
	h := newCoinbaseHandler(em)
	body := cbCharge("charge:confirmed", "CH-1", "order-1", "50.00")

	rec := doSigned(h, body, cbSign(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed: status = %d, want 200", rec.Code)
	}
	if len(em.events) != 1 {
		t.Fatalf("confirmed: emitted %d events, want 1", len(em.events))
	}
	got := em.events[0]
	if got.GetPaymentProvider() != "coinbase" || got.GetEventType() != "recharge_paid" {
		t.Errorf("emitted provider/type = %q/%q, want coinbase/recharge_paid", got.GetPaymentProvider(), got.GetEventType())
	}
	if got.GetOrderId() != "order-1" || got.GetSettledAmount() != "50.00" {
		t.Errorf("emitted order/settled = %q/%q, want order-1/50.00", got.GetOrderId(), got.GetSettledAmount())
	}
	if got.GetUserId() != "" {
		t.Errorf("event must not carry a client user_id, got %q (BR-A-3)", got.GetUserId())
	}
}

// 7.4-INT-012 (boundary) — a valid charge:pending verifies but is a no-op: 200 ACK,
// NOTHING emitted (no credit on unconfirmed funds, BR-C-1).
func TestCoinbaseIngress_Pending_NoEmit(t *testing.T) {
	em := &fakeEmitter{}
	h := newCoinbaseHandler(em)
	body := cbCharge("charge:pending", "CH-1", "order-1", "50.00")

	rec := doSigned(h, body, cbSign(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("pending: status = %d, want 200 (ACK no-op)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("pending: emitter called %d times, want 0 (no credit on unconfirmed)", em.callerCt)
	}
}

// 7.4-INT-024 (transient lane) — a verified event whose produce FAILS returns 5xx
// so Coinbase redelivers (at-least-once + idempotent downstream, BR-W-5).
func TestCoinbaseIngress_EmitFails_5xx(t *testing.T) {
	em := &fakeEmitter{emitErr: errors.New("kafka down")}
	h := newCoinbaseHandler(em)
	body := cbCharge("charge:confirmed", "CH-1", "order-1", "50.00")

	rec := doSigned(h, body, cbSign(body))
	if rec.Code < 500 {
		t.Fatalf("emit-fail: status = %d, want 5xx (provider redelivers, BR-W-5)", rec.Code)
	}
}
