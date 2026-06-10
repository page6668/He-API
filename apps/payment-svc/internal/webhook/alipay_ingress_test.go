// Story 7.5 — alipay webhook ingress through the REAL Handler + the REAL
// alipay.Provider (not a fakeProvider): the offline integration proof for the
// lanes the QA skeleton points here (7.5-INT-022 forged->400 + zero side-effect,
// 7.5-INT-025/026 2xx/4xx/5xx discipline, the success-credits-once and
// in-progress-no-credit boundaries at the producer boundary). The PG state-machine
// (replay/idempotency/atomicity) + the CNY→USD fx-at-credit are exercised in
// billing-svc (reused from 7.3/7.2). RSA fixtures use an ephemeral in-test keypair.
package webhook

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/alipay"
)

const apClientID = "SANDBOX_alipay_ingress"

var apNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func apKeys(t *testing.T) (priv *rsa.PrivateKey, privPEM, pubPEM string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pd, _ := x509.MarshalPKCS8PrivateKey(priv)
	pubder, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pd}))
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubder}))
	return
}

func apNotif(resultStatus, paymentID, orderID, currency, minor string) []byte {
	b, _ := json.Marshal(map[string]any{
		"result":           map[string]string{"resultStatus": resultStatus},
		"paymentId":        paymentID,
		"paymentRequestId": orderID,
		"paymentAmount":    map[string]string{"currency": currency, "value": minor},
	})
	return b
}

func apSign(t *testing.T, priv *rsa.PrivateKey, notifyPath, requestTime string, body []byte) string {
	t.Helper()
	signStr := "POST " + notifyPath + "\n" + apClientID + "." + requestTime + "." + string(body)
	d := sha256.Sum256([]byte(signStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, d[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return "algorithm=RSA256,keyVersion=1,signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
}

func newAlipayHandler(t *testing.T, em Emitter, privPEM, pubPEM string) *Handler {
	t.Helper()
	p := alipay.New(apClientID, privPEM, pubPEM, alipay.WithClock(func() time.Time { return apNow }))
	reg := provider.NewRegistry(p)
	return New(reg, em, nil)
}

func doAlipay(h *Handler, body []byte, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/alipay", strings.NewReader(string(body)))
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.Handle("alipay")(rec, req)
	return rec
}

func apValidHeaders(t *testing.T, priv *rsa.PrivateKey, body []byte) http.Header {
	rt := apNow.Format(time.RFC3339)
	h := http.Header{}
	h.Set("Client-Id", apClientID)
	h.Set("Request-Time", rt)
	h.Set("Signature", apSign(t, priv, alipay.DefaultNotifyPath, rt, body))
	return h
}

// 7.5-INT-022 — a forged signature is rejected at the handler with 400 and NOTHING
// is emitted (zero downstream side effect → zero recharge_orders mutation / credit).
func TestAlipayIngress_Forged_Rejected(t *testing.T) {
	priv, privPEM, pubPEM := apKeys(t)
	em := &fakeEmitter{}
	h := newAlipayHandler(t, em, privPEM, pubPEM)
	body := apNotif("S", "PAY-1", "order-1", "USD", "5000")

	hdr := apValidHeaders(t, priv, body)
	hdr.Set("Signature", "algorithm=RSA256,keyVersion=1,signature="+url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("garbage"))))
	rec := doAlipay(h, body, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged: status = %d, want 400", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("forged: emitter called %d times, want 0 (body never trusted, BR-W-1)", em.callerCt)
	}
}

// 7.5-INT (genuine lane) + success-credits-once boundary — a valid resultStatus=S
// verifies and emits exactly one recharge_paid PaymentEvent carrying the
// provider-confirmed settled amount + provider="alipay" (CNY → minor→decimal).
func TestAlipayIngress_Success_EmitsOnce(t *testing.T) {
	priv, privPEM, pubPEM := apKeys(t)
	em := &fakeEmitter{}
	h := newAlipayHandler(t, em, privPEM, pubPEM)
	body := apNotif("S", "PAY-1", "order-1", "CNY", "35000")

	rec := doAlipay(h, body, apValidHeaders(t, priv, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("success: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(em.events) != 1 {
		t.Fatalf("success: emitted %d events, want 1", len(em.events))
	}
	got := em.events[0]
	if got.GetPaymentProvider() != "alipay" || got.GetEventType() != "recharge_paid" {
		t.Errorf("emitted provider/type = %q/%q, want alipay/recharge_paid", got.GetPaymentProvider(), got.GetEventType())
	}
	if got.GetOrderId() != "order-1" || got.GetSettledAmount() != "350.00" || got.GetCurrency() != "CNY" {
		t.Errorf("emitted order/settled/currency = %q/%q/%q, want order-1/350.00/CNY", got.GetOrderId(), got.GetSettledAmount(), got.GetCurrency())
	}
}

// 7.5-INT — resultStatus=U (in-progress) verifies but is a no-op: 200 ACK, NOTHING
// emitted (no credit on a non-terminal result, BR-C-1).
func TestAlipayIngress_InProgress_NoEmit(t *testing.T) {
	priv, privPEM, pubPEM := apKeys(t)
	em := &fakeEmitter{}
	h := newAlipayHandler(t, em, privPEM, pubPEM)
	body := apNotif("U", "PAY-1", "order-1", "USD", "5000")

	rec := doAlipay(h, body, apValidHeaders(t, priv, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("in-progress: status = %d, want 200 (ACK no-op)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("in-progress: emitter called %d times, want 0 (no credit on U)", em.callerCt)
	}
}

// 7.5-INT-024 (replay layer 1) — a captured valid success with a STALE Request-Time
// is rejected by the freshness window before any emit (BR-W-4).
func TestAlipayIngress_StaleReplay_Rejected(t *testing.T) {
	priv, privPEM, pubPEM := apKeys(t)
	em := &fakeEmitter{}
	h := newAlipayHandler(t, em, privPEM, pubPEM)
	body := apNotif("S", "PAY-1", "order-1", "USD", "5000")

	staleRT := apNow.Add(-10 * time.Minute).Format(time.RFC3339)
	hdr := http.Header{}
	hdr.Set("Client-Id", apClientID)
	hdr.Set("Request-Time", staleRT)
	hdr.Set("Signature", apSign(t, priv, alipay.DefaultNotifyPath, staleRT, body))

	rec := doAlipay(h, body, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stale replay: status = %d, want 400 (freshness reject)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("stale replay: emitter called %d times, want 0", em.callerCt)
	}
}
