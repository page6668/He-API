// Story 7.6 — wechat webhook ingress through the REAL Handler + the REAL
// wechat.Provider (not a fakeProvider): the offline integration proof for the
// lanes the QA skeleton points here (7.6-INT-022 forged->400 + zero side-effect,
// 7.6-INT-025/026 2xx/4xx discipline, the success-credits-once + in-progress-no-
// credit boundaries + the AES-GCM tampered-ciphertext fail-closed at the producer
// boundary). The PG state-machine (replay/idempotency/atomicity) + the HKD/CNY→USD
// fx-at-credit are exercised in billing-svc (REUSED from 7.3/7.2/7.5, credit.go
// UNCHANGED). RSA fixtures use an ephemeral in-test keypair + a test APIv3Key.
package webhook

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	"github.com/he-api/he-api/apps/payment-svc/internal/provider/wechat"
)

const (
	wxMchID      = "MCH-WX-INGRESS"
	wxCertSerial = "MERCHANT_SERIAL_WX"
	wxPlatSerial = "PLATFORM_SERIAL_WX"
	wxAPIV3Key   = "0123456789abcdef0123456789abcdef" // 32B AES-256
	wxHdrNonce   = "wx-ingress-header-nonce"
	wxGCMNonce   = "0123456789ab" // 12B
)

var wxNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func wxKeys(t *testing.T) (priv *rsa.PrivateKey, privPEM, pubPEM string) {
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

func wxEncrypt(t *testing.T, key []byte, nonce, aad string, plaintext []byte) string {
	t.Helper()
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	return base64.StdEncoding.EncodeToString(gcm.Seal(nil, []byte(nonce), plaintext, []byte(aad)))
}

func wxTx(outTradeNo, txID, tradeState, currency string, total int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"out_trade_no": outTradeNo, "transaction_id": txID, "trade_state": tradeState,
		"amount": map[string]any{"total": total, "currency": currency},
	})
	return b
}

func wxCallbackBody(eventType, ciphertextB64, nonce, aad string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "EV-1", "event_type": eventType, "resource_type": "encrypt-resource",
		"resource": map[string]any{
			"algorithm": "AEAD_AES_256_GCM", "ciphertext": ciphertextB64,
			"nonce": nonce, "associated_data": aad, "original_type": "transaction",
		},
	})
	return b
}

func wxSign(t *testing.T, priv *rsa.PrivateKey, timestamp, nonce string, body []byte) string {
	t.Helper()
	d := sha256.Sum256([]byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n"))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, d[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func newWechatHandler(t *testing.T, em Emitter, privPEM, pubPEM string) *Handler {
	t.Helper()
	p := wechat.New(wxMchID, privPEM, wxCertSerial, pubPEM, wxPlatSerial, wxAPIV3Key,
		wechat.WithClock(func() time.Time { return wxNow }))
	return New(provider.NewRegistry(p), em, nil)
}

func doWechat(h *Handler, body []byte, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/wechat", strings.NewReader(string(body)))
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.Handle("wechat")(rec, req)
	return rec
}

// wxValid builds a full valid callback (body + headers) for a transaction.
func wxValid(t *testing.T, priv *rsa.PrivateKey, eventType, tradeState, outTradeNo, currency string, total int64) ([]byte, http.Header) {
	t.Helper()
	ct := wxEncrypt(t, []byte(wxAPIV3Key), wxGCMNonce, "transaction", wxTx(outTradeNo, "TX-1", tradeState, currency, total))
	body := wxCallbackBody(eventType, ct, wxGCMNonce, "transaction")
	ts := strconv.FormatInt(wxNow.Unix(), 10)
	h := http.Header{}
	h.Set("Wechatpay-Signature", wxSign(t, priv, ts, wxHdrNonce, body))
	h.Set("Wechatpay-Timestamp", ts)
	h.Set("Wechatpay-Nonce", wxHdrNonce)
	h.Set("Wechatpay-Serial", wxPlatSerial)
	return body, h
}

// 7.6-INT-022 — a forged signature is rejected at the handler with 400 and NOTHING
// is emitted (zero downstream side effect → zero recharge_orders mutation / credit).
func TestWechatIngress_Forged_Rejected(t *testing.T) {
	priv, privPEM, pubPEM := wxKeys(t)
	em := &fakeEmitter{}
	h := newWechatHandler(t, em, privPEM, pubPEM)
	body, hdr := wxValid(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "order-1", "USD", 5000)
	hdr.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString([]byte("garbage")))
	rec := doWechat(h, body, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged: status = %d, want 400", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("forged: emitter called %d times, want 0 (body never trusted, BR-W-1)", em.callerCt)
	}
}

// 7.6-INT (genuine lane) + success-credits-once boundary — a valid SUCCESS verifies,
// decrypts, and emits exactly one recharge_paid PaymentEvent carrying the provider-
// confirmed settled amount + provider="wechat" (HKD → minor→decimal, 港澳 path).
func TestWechatIngress_Success_EmitsOnce(t *testing.T) {
	priv, privPEM, pubPEM := wxKeys(t)
	em := &fakeEmitter{}
	h := newWechatHandler(t, em, privPEM, pubPEM)
	body, hdr := wxValid(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "order-1", "HKD", 39000)

	rec := doWechat(h, body, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("success: status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if len(em.events) != 1 {
		t.Fatalf("success: emitted %d events, want 1", len(em.events))
	}
	got := em.events[0]
	if got.GetPaymentProvider() != "wechat" || got.GetEventType() != "recharge_paid" {
		t.Errorf("emitted provider/type = %q/%q, want wechat/recharge_paid", got.GetPaymentProvider(), got.GetEventType())
	}
	if got.GetOrderId() != "order-1" || got.GetSettledAmount() != "390.00" || got.GetCurrency() != "HKD" {
		t.Errorf("emitted order/settled/currency = %q/%q/%q, want order-1/390.00/HKD", got.GetOrderId(), got.GetSettledAmount(), got.GetCurrency())
	}
}

// 7.6-INT — trade_state=NOTPAY (in-progress) verifies but is a no-op: 200 ACK,
// NOTHING emitted (no credit on a non-terminal result, BR-C-1).
func TestWechatIngress_InProgress_NoEmit(t *testing.T) {
	priv, privPEM, pubPEM := wxKeys(t)
	em := &fakeEmitter{}
	h := newWechatHandler(t, em, privPEM, pubPEM)
	body, hdr := wxValid(t, priv, "TRANSACTION.SUCCESS", "NOTPAY", "order-1", "USD", 5000)

	rec := doWechat(h, body, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("in-progress: status = %d, want 200 (ACK no-op)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("in-progress: emitter called %d times, want 0 (no credit on NOTPAY)", em.callerCt)
	}
}

// 7.6-INT-023 (⭐ encryption layer) — a genuine OUTER signature over a TAMPERED
// ciphertext → AES-GCM tag mismatch → 400, NOTHING emitted (fail-closed, BR-W-3).
func TestWechatIngress_TamperedCiphertext_Rejected(t *testing.T) {
	priv, privPEM, pubPEM := wxKeys(t)
	em := &fakeEmitter{}
	h := newWechatHandler(t, em, privPEM, pubPEM)
	ct := wxEncrypt(t, []byte(wxAPIV3Key), wxGCMNonce, "transaction", wxTx("order-1", "TX", "SUCCESS", "USD", 5000))
	raw, _ := base64.StdEncoding.DecodeString(ct)
	raw[0] ^= 0xFF
	body := wxCallbackBody("TRANSACTION.SUCCESS", base64.StdEncoding.EncodeToString(raw), wxGCMNonce, "transaction")
	ts := strconv.FormatInt(wxNow.Unix(), 10)
	hdr := http.Header{}
	hdr.Set("Wechatpay-Signature", wxSign(t, priv, ts, wxHdrNonce, body)) // valid outer sig
	hdr.Set("Wechatpay-Timestamp", ts)
	hdr.Set("Wechatpay-Nonce", wxHdrNonce)
	hdr.Set("Wechatpay-Serial", wxPlatSerial)

	rec := doWechat(h, body, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tampered ciphertext: status = %d, want 400 (GCM tag mismatch, fail-closed)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("tampered ciphertext: emitter called %d times, want 0", em.callerCt)
	}
}

// 7.6-INT-024 (replay layer 1) — a captured valid SUCCESS with a STALE
// Wechatpay-Timestamp is rejected by the freshness window before any emit (BR-W-5).
func TestWechatIngress_StaleReplay_Rejected(t *testing.T) {
	priv, privPEM, pubPEM := wxKeys(t)
	em := &fakeEmitter{}
	h := newWechatHandler(t, em, privPEM, pubPEM)
	ct := wxEncrypt(t, []byte(wxAPIV3Key), wxGCMNonce, "transaction", wxTx("order-1", "TX", "SUCCESS", "USD", 5000))
	body := wxCallbackBody("TRANSACTION.SUCCESS", ct, wxGCMNonce, "transaction")
	staleTS := strconv.FormatInt(wxNow.Add(-10*time.Minute).Unix(), 10)
	hdr := http.Header{}
	hdr.Set("Wechatpay-Signature", wxSign(t, priv, staleTS, wxHdrNonce, body))
	hdr.Set("Wechatpay-Timestamp", staleTS)
	hdr.Set("Wechatpay-Nonce", wxHdrNonce)
	hdr.Set("Wechatpay-Serial", wxPlatSerial)

	rec := doWechat(h, body, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("stale replay: status = %d, want 400 (freshness reject)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("stale replay: emitter called %d times, want 0", em.callerCt)
	}
}
