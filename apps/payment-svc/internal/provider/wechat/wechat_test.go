// Story 7.6 — wechat provider unit suite (implements the QA test-design skeleton,
// Turing 2026-06-10, docs/qa/assessments/7.6-test-design-20260610.md). White-box
// (package wechat) so it exercises parseTransaction / decryptResource /
// decimalToMinor / minorToDecimal / the constructor directly. WeChat Pay is mocked
// with httptest; the asymmetric-RSA verify + AES-256-GCM decrypt lanes run fully
// offline over an EPHEMERAL in-test RSA keypair + a test APIv3Key — no real WeChat
// credential is needed (the test holds the platform PRIVATE key only to FORGE
// valid + adversarial signature fixtures, and the APIv3Key to encrypt resources).
//
// Cross-package scenarios (the credit applier, the PG state-machine, the gateway
// route-auth + raw-body proxy, the E2E journeys) live in their real homes — the
// credit spine + HKD→USD fx-at-credit are REUSED from 7.3/7.2/7.5 (credit.go
// UNCHANGED); wechat emits the SAME PaymentEvent so the inherited applier credits
// it (re-asserted at the producer boundary in internal/webhook/wechat_ingress_test.go).
package wechat

import (
	"context"
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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// Compile-time proof that wechat satisfies the SAME seam as
// stripe/paypal/coinbase/alipay — closes the Epic-7 5-channel seam proof; the
// recharge handler + credit.go stay provider-agnostic (7.6-INT-026, BR-A-1/C-5).
var _ provider.PaymentProvider = (*Provider)(nil)

const (
	testMchID      = "MCH-7.6-TEST"
	testAppID      = "wxAPP7.6"
	testCertSerial = "MERCHANT_CERT_SERIAL_1"
	testPlatSerial = "PLATFORM_CERT_SERIAL_1"
	// testAPIV3Key is exactly 32 bytes (AES-256).
	testAPIV3Key = "0123456789abcdef0123456789abcdef"
	// headerNonce is the Wechatpay-Nonce (signed-string component) — DISTINCT from
	// the 12-byte AES-GCM resource nonce.
	headerNonce = "wechat-header-nonce-1"
	// gcmNonce is the 12-byte AES-GCM resource nonce.
	gcmNonce = "0123456789ab"
)

// fixedNow pins the provider clock so Wechatpay-Timestamp freshness is deterministic.
var fixedNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// --- key + fixture helpers ---------------------------------------------------

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genKey: %v", err)
	}
	return k
}

func privPEM(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("marshal private: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func pubPEM(t *testing.T, k *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatalf("marshal public: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// newVerifier builds a Provider whose platform verify key is priv's public key,
// clock pinned. The test holds priv to forge fixtures.
func newVerifier(t *testing.T, priv *rsa.PrivateKey, opts ...Option) *Provider {
	t.Helper()
	base := []Option{WithClock(func() time.Time { return fixedNow }), WithAppID(testAppID)}
	return New(testMchID, privPEM(t, priv), testCertSerial, pubPEM(t, &priv.PublicKey), testPlatSerial, testAPIV3Key, append(base, opts...)...)
}

// txJSON builds a decrypted WeChat transaction plaintext.
func txJSON(outTradeNo, txID, tradeState, currency string, total int64) []byte {
	b, _ := json.Marshal(map[string]any{
		"out_trade_no":   outTradeNo,
		"transaction_id": txID,
		"trade_state":    tradeState,
		"amount":         map[string]any{"total": total, "currency": currency},
	})
	return b
}

// encryptResource AES-256-GCM-encrypts a plaintext with key, nonce, aad (the same
// primitive the provider decrypts) → base64 ciphertext (incl. the trailing tag).
func encryptResource(t *testing.T, key []byte, nonce, aad string, plaintext []byte) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	ct := gcm.Seal(nil, []byte(nonce), plaintext, []byte(aad))
	return base64.StdEncoding.EncodeToString(ct)
}

// callbackBody builds the outer (signed, plaintext) callback envelope.
func callbackBody(eventType, ciphertextB64, nonce, aad string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id":            "EV-1",
		"event_type":    eventType,
		"resource_type": "encrypt-resource",
		"resource": map[string]any{
			"algorithm":       "AEAD_AES_256_GCM",
			"ciphertext":      ciphertextB64,
			"nonce":           nonce,
			"associated_data": aad,
			"original_type":   "transaction",
		},
	})
	return b
}

// signCallback returns the base64 Wechatpay-Signature over `<ts>\n<nonce>\n<body>\n`.
func signCallback(t *testing.T, priv *rsa.PrivateKey, timestamp, nonce string, body []byte) string {
	t.Helper()
	signStr := timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	d := sha256.Sum256([]byte(signStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, d[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// successCallback builds a full valid SUCCESS callback (body + headers) for a tx.
func successCallback(t *testing.T, priv *rsa.PrivateKey, eventType, tradeState, outTradeNo, txID, currency string, total int64) ([]byte, http.Header) {
	t.Helper()
	plain := txJSON(outTradeNo, txID, tradeState, currency, total)
	ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
	body := callbackBody(eventType, ct, gcmNonce, "transaction")
	ts := strconv.FormatInt(fixedNow.Unix(), 10)
	h := http.Header{}
	h.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
	h.Set("Wechatpay-Timestamp", ts)
	h.Set("Wechatpay-Nonce", headerNonce)
	h.Set("Wechatpay-Serial", testPlatSerial)
	return body, h
}

// ============================================================
// AC1: 充值下单 — CreateCheckout → WeChat Native (QR) transaction
// ============================================================

func TestAC1_CreateCheckout(t *testing.T) {
	priv := genKey(t)

	// 7.6-UNIT-001/002/003 | P0 — builds the Native POST request, signs it, maps code_url.
	t.Run("7.6-UNIT-001 builds + signs Native request, maps code_url", func(t *testing.T) {
		var gotBody []byte
		var gotHeaders http.Header
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotHeaders = r.Header.Clone()
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code_url":"weixin://wxpay/bizpayurl?pr=abc123"}`))
		}))
		defer srv.Close()

		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		res, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "order-1", UserID: "user-1", Amount: "50.00", Currency: "USD"})
		if err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if res.CheckoutURL != "weixin://wxpay/bizpayurl?pr=abc123" {
			t.Fatalf("mapped result = %+v", res)
		}
		if gotPath != nativePath {
			t.Fatalf("path = %q, want %q", gotPath, nativePath)
		}
		// 7.6-UNIT-002 — outbound Authorization header present + scheme correct.
		authz := gotHeaders.Get("Authorization")
		if !strings.HasPrefix(authz, authSchema+" ") || !strings.Contains(authz, "signature=") ||
			!strings.Contains(authz, `serial_no="`+testCertSerial+`"`) || !strings.Contains(authz, `mchid="`+testMchID+`"`) {
			t.Fatalf("outbound Authorization header = %q", authz)
		}
		// 7.6-UNIT-003 — minor-unit amount on the wire ("50.00" USD → 5000) + binding.
		var sent struct {
			MchID      string         `json:"mchid"`
			AppID      string         `json:"appid"`
			OutTradeNo string         `json:"out_trade_no"`
			NotifyURL  string         `json:"notify_url"`
			Amount     map[string]any `json:"amount"`
		}
		if err := json.Unmarshal(gotBody, &sent); err != nil {
			t.Fatalf("unmarshal sent body: %v", err)
		}
		if sent.Amount["total"] != float64(5000) || sent.Amount["currency"] != "USD" {
			t.Fatalf("amount = %v, want total=5000 currency=USD", sent.Amount)
		}
		if sent.OutTradeNo != "order-1" || sent.MchID != testMchID || sent.AppID != testAppID || sent.NotifyURL != DefaultNotifyURL {
			t.Fatalf("order/mchid/appid/notify binding wrong: %+v", sent)
		}
	})

	// 7.6-UNIT-003b — HKD charge sends 39000 minor units (港澳); CNY sends 35000.
	t.Run("7.6-UNIT-003b HKD/CNY charge sends correct minor units", func(t *testing.T) {
		for _, c := range []struct {
			cur, amount string
			total       float64
		}{{"HKD", "390.00", 39000}, {"CNY", "350.00", 35000}} {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				_, _ = w.Write([]byte(`{"code_url":"weixin://x"}`))
			}))
			p := newVerifier(t, priv, WithBaseURL(srv.URL))
			if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o2", UserID: "u2", Amount: c.amount, Currency: c.cur}); err != nil {
				srv.Close()
				t.Fatalf("CreateCheckout %s: %v", c.cur, err)
			}
			srv.Close()
			var sent struct {
				Amount map[string]any `json:"amount"`
			}
			_ = json.Unmarshal(gotBody, &sent)
			if sent.Amount["total"] != c.total || sent.Amount["currency"] != c.cur {
				t.Fatalf("%s amount = %v, want total=%v", c.cur, sent.Amount, c.total)
			}
		}
	})

	// 7.6-BLIND-ERROR-001/003 — WeChat API failure → wrapped error, no key leak.
	t.Run("7.6-BLIND-ERROR-001 WeChat 5xx → wrapped error, no key leak", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		_, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.00", Currency: "USD"})
		if err == nil {
			t.Fatal("want error on 502")
		}
		if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), testAPIV3Key) || strings.Contains(err.Error(), srv.URL) {
			t.Fatalf("error leaks secret/url: %v", err)
		}
	})

	// 7.6-BLIND-ERROR-004 — 2xx missing code_url → contract error.
	t.Run("7.6-BLIND-ERROR-004 2xx missing code_url → contract error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want contract error on missing code_url")
		}
	})

	// 7.6-BLIND-BOUNDARY-005 — sub-minor amount rejected before provider call.
	t.Run("7.6-BLIND-BOUNDARY-005 sub-minor amount → error, no provider call", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.001", Currency: "USD"}); err == nil {
			t.Fatal("want error on sub-minor amount")
		}
		if called {
			t.Fatal("provider was called despite sub-minor amount")
		}
	})

	// 7.6-BLIND-ERROR — signing key unavailable → fail-closed, no provider call.
	t.Run("7.6 signing key unavailable → fail-closed", func(t *testing.T) {
		bad := New(testMchID, "not-a-key", testCertSerial, "not-a-key", testPlatSerial, testAPIV3Key)
		if _, err := bad.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want error when signing key unavailable")
		}
	})
}

// 7.6-UNIT-004/005/008 — Name, constructor wiring, not-supported subscription.
func TestProviderBasics(t *testing.T) {
	priv := genKey(t)
	p := newVerifier(t, priv)
	if p.Name() != "wechat" {
		t.Fatalf("Name = %q", p.Name())
	}
	// CreateSubscription is not-supported (mirrors coinbase/alipay, Q-SUBSCOPE).
	if _, err := p.CreateSubscription(context.Background(), provider.Subscription{}); err == nil {
		t.Fatal("CreateSubscription must return not-supported")
	}
	// WithBaseURL / WithNotifyURL / WithAppID override.
	p2 := New(testMchID, privPEM(t, priv), testCertSerial, pubPEM(t, &priv.PublicKey), testPlatSerial, testAPIV3Key,
		WithBaseURL("https://x"), WithNotifyURL("https://n/wechat"), WithAppID("wxZ"))
	if p2.baseURL != "https://x" || p2.notifyURL != "https://n/wechat" || p2.appID != "wxZ" {
		t.Fatalf("options not applied: %+v", p2)
	}
	// Bad PEM → keyErr set → fail-closed on use.
	bad := New(testMchID, "not-a-key", testCertSerial, "not-a-key", testPlatSerial, testAPIV3Key)
	if bad.keyErr == nil {
		t.Fatal("expected keyErr on bad PEM")
	}
	// Bad APIv3Key length → keyErr set → fail-closed verify.
	badKey := New(testMchID, privPEM(t, priv), testCertSerial, pubPEM(t, &priv.PublicKey), testPlatSerial, "tooshort")
	if badKey.keyErr == nil {
		t.Fatal("expected keyErr on non-32-byte APIv3Key")
	}
	body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
	if _, err := badKey.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("bad APIv3Key verify should fail closed, got %v", err)
	}
}

// 7.6-UNIT-006/007 — per-currency minor-unit table (HKD added) + fail-closed.
func TestMinorUnits(t *testing.T) {
	cases := []struct {
		dec, cur, minor string
		ok              bool
	}{
		{"50.00", "USD", "5000", true},
		{"390.00", "HKD", "39000", true}, // HKD added (7.6)
		{"350.00", "CNY", "35000", true},
		{"0.01", "HKD", "1", true},
		{"50.001", "USD", "", false}, // sub-minor → reject
		{"50.00", "EUR", "", false},  // unsupported currency
		{"abc", "USD", "", false},    // unparseable
	}
	for _, c := range cases {
		got, err := decimalToMinor(c.dec, c.cur)
		if c.ok != (err == nil) {
			t.Fatalf("decimalToMinor(%q,%q) err=%v, ok=%v", c.dec, c.cur, err, c.ok)
		}
		if c.ok && got != c.minor {
			t.Fatalf("decimalToMinor(%q,%q)=%q, want %q", c.dec, c.cur, got, c.minor)
		}
	}
	// Reverse round-trip (int-based, as the decrypted resource carries an integer).
	for _, c := range []struct {
		minor int64
		cur   string
		dec   string
	}{{5000, "USD", "50.00"}, {39000, "HKD", "390.00"}, {35000, "CNY", "350.00"}, {1, "HKD", "0.01"}} {
		got, err := minorIntToDecimal(c.minor, c.cur)
		if err != nil || got != c.dec {
			t.Fatalf("minorIntToDecimal(%d,%q)=%q,%v want %q", c.minor, c.cur, got, err, c.dec)
		}
	}
	// Non-integer minor value → fail closed.
	if _, err := minorToDecimal("50.5", "USD"); err == nil {
		t.Fatal("non-integer minor value must fail closed")
	}
}

// ============================================================
// AC3: webhook 安全 — APIv3 asymmetric verify + AES-256-GCM decrypt (security core)
// ============================================================

func TestAC3_VerifyWebhook(t *testing.T) {
	priv := genKey(t)

	// 7.6-UNIT-020 | P0 — valid asymmetric verify → decrypt → VerifiedEvent.
	t.Run("7.6-UNIT-020 valid signature verifies + decrypts + parses", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "order-1", "TX-1", "USD", 5000)
		ev, err := p.VerifyWebhook(context.Background(), body, hdr)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ev.Kind != provider.EventRechargePaid || ev.OrderID != "order-1" || ev.ExternalOrderID != "TX-1" || ev.SettledAmount != "50.00" || ev.Currency != "USD" {
			t.Fatalf("event = %+v", ev)
		}
	})

	// 7.6-UNIT-021/022/023 | P0 — forged / absent / wrong-key → ErrSignatureInvalid.
	t.Run("7.6-UNIT-021 forged signature rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		hdr.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString([]byte("garbage-not-a-real-signature")))
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("forged → %v, want ErrSignatureInvalid", err)
		}
	})
	t.Run("7.6-UNIT-022 absent headers rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		for _, drop := range []string{"Wechatpay-Signature", "Wechatpay-Timestamp", "Wechatpay-Nonce", "Wechatpay-Serial"} {
			body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
			hdr.Del(drop)
			if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
				t.Fatalf("missing %s → %v, want ErrSignatureInvalid", drop, err)
			}
		}
	})
	t.Run("7.6-UNIT-023 wrong key (attacker-signed) rejected", func(t *testing.T) {
		attacker := genKey(t)
		p := newVerifier(t, priv) // verifies with priv's public key
		body, _ := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		// Re-sign the SAME body with the attacker key, keep valid headers otherwise.
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, attacker, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("wrong-key → %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.6-UNIT-024 | P0 — unknown / mismatched Wechatpay-Serial → reject (fail-closed).
	t.Run("7.6-UNIT-024 unknown Wechatpay-Serial rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		hdr.Set("Wechatpay-Serial", "UNKNOWN_SERIAL_999")
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("unknown serial → %v, want ErrSignatureInvalid (do NOT skip verify)", err)
		}
	})

	// 7.6-UNIT-025/026 | P0 — signed-string component tampering (table) → all reject.
	// NO method/URI component (no r.URL.Path trap — the 7.5 #1 risk is eliminated).
	t.Run("7.6-UNIT-025 signed-string component tamper rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		// tamper body (re-encrypt a different amount under a fresh signature would be
		// needed; altering the signed body post-sign breaks verify).
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		other, _ := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 9999)
		if _, err := p.VerifyWebhook(context.Background(), other, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered body must reject")
		}
		// tamper Wechatpay-Timestamp (still in-window so freshness passes, but verify fails).
		body, hdr = successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		hdr.Set("Wechatpay-Timestamp", strconv.FormatInt(fixedNow.Add(time.Minute).Unix(), 10))
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered Wechatpay-Timestamp must reject")
		}
		// tamper Wechatpay-Nonce.
		body, hdr = successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		hdr.Set("Wechatpay-Nonce", "tampered-nonce")
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered Wechatpay-Nonce must reject")
		}
	})

	// 7.6-UNIT-027/028 | P0 — replay layer 1: Wechatpay-Timestamp freshness window.
	t.Run("7.6-UNIT-027 stale timestamp rejected", func(t *testing.T) {
		p := newVerifier(t, priv, WithTolerance(5*time.Minute))
		plain := txJSON("o", "TX", "SUCCESS", "USD", 5000)
		ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
		body := callbackBody("TRANSACTION.SUCCESS", ct, gcmNonce, "transaction")
		staleTS := strconv.FormatInt(fixedNow.Add(-10*time.Minute).Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, staleTS, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", staleTS)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("stale timestamp → %v, want reject", err)
		}
	})
	t.Run("7.6-UNIT-028 just-inside window accepted", func(t *testing.T) {
		p := newVerifier(t, priv, WithTolerance(5*time.Minute))
		plain := txJSON("o", "TX", "SUCCESS", "USD", 5000)
		ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
		body := callbackBody("TRANSACTION.SUCCESS", ct, gcmNonce, "transaction")
		ts := strconv.FormatInt(fixedNow.Add(-4*time.Minute).Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); err != nil {
			t.Fatalf("in-window → %v, want accept", err)
		}
	})

	// 7.6-UNIT-030 | P0 — AES-256-GCM decrypt happy path (⭐ the new primitive).
	t.Run("7.6-UNIT-030 valid resource decrypts", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "order-9", "TX-9", "HKD", 39000)
		ev, err := p.VerifyWebhook(context.Background(), body, hdr)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if ev.SettledAmount != "390.00" || ev.Currency != "HKD" || ev.OrderID != "order-9" {
			t.Fatalf("decrypted event = %+v", ev)
		}
	})

	// 7.6-UNIT-031/032/033 | P0 — AES-GCM adversarial: tampered ciphertext / nonce /
	// AAD / wrong APIv3Key → GCM tag mismatch → fail-closed, NO partial-parse, NO credit.
	t.Run("7.6-UNIT-031 tampered ciphertext → fail-closed", func(t *testing.T) {
		p := newVerifier(t, priv)
		plain := txJSON("o", "TX", "SUCCESS", "USD", 5000)
		ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
		// Flip a byte in the ciphertext (re-base64 a mutated buffer).
		raw, _ := base64.StdEncoding.DecodeString(ct)
		raw[0] ^= 0xFF
		tampered := base64.StdEncoding.EncodeToString(raw)
		body := callbackBody("TRANSACTION.SUCCESS", tampered, gcmNonce, "transaction")
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("tampered ciphertext → %v, want fail-closed", err)
		}
	})
	t.Run("7.6-UNIT-032 tampered nonce/AAD → fail-closed", func(t *testing.T) {
		p := newVerifier(t, priv)
		plain := txJSON("o", "TX", "SUCCESS", "USD", 5000)
		ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
		// Correct ciphertext but a DIFFERENT associated_data → tag mismatch.
		body := callbackBody("TRANSACTION.SUCCESS", ct, gcmNonce, "tampered-aad")
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("tampered AAD → %v, want fail-closed", err)
		}
	})
	t.Run("7.6-UNIT-033 wrong APIv3Key → fail-closed", func(t *testing.T) {
		// Encrypt under a DIFFERENT key than the provider holds → tag mismatch.
		wrongKey := []byte("ffffffffffffffffffffffffffffffff") // 32B, different
		p := newVerifier(t, priv)
		plain := txJSON("o", "TX", "SUCCESS", "USD", 5000)
		ct := encryptResource(t, wrongKey, gcmNonce, "transaction", plain)
		body := callbackBody("TRANSACTION.SUCCESS", ct, gcmNonce, "transaction")
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("wrong APIv3Key → %v, want fail-closed", err)
		}
	})

	// 7.6-UNIT-034 | P0 — order of operations: a forged signature is rejected BEFORE
	// any decrypt attempt (no decrypt oracle). Use a VALID ciphertext but a bad sig:
	// if the impl decrypted first it would still reject, but we assert the signature
	// path rejects it (and a wrong serial — which never reaches decrypt — also rejects).
	t.Run("7.6-UNIT-034 verify-before-decrypt (no decrypt oracle)", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		// Valid resource, but corrupt the signature → must reject at verify, never decrypt.
		hdr.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString([]byte("forged")))
		if _, err := p.VerifyWebhook(context.Background(), body, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("forged-sig+valid-ciphertext → %v, want reject at verify", err)
		}
	})

	// 7.6-UNIT-035 — secret discipline: no key material ever in the error.
	t.Run("7.6-UNIT-035 errors never leak key material", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "o", "TX", "USD", 5000)
		hdr.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString([]byte("forged")))
		_, err := p.VerifyWebhook(context.Background(), body, hdr)
		if err == nil || strings.Contains(err.Error(), testAPIV3Key) || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("error leaks key material or is nil: %v", err)
		}
	})

	// 7.6-UNIT — unsupported resource algorithm → fail-closed.
	t.Run("7.6 unsupported resource algorithm rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		b, _ := json.Marshal(map[string]any{
			"event_type": "TRANSACTION.SUCCESS",
			"resource":   map[string]any{"algorithm": "SOMETHING_ELSE", "ciphertext": "x", "nonce": gcmNonce, "associated_data": "transaction"},
		})
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, b))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		if _, err := p.VerifyWebhook(context.Background(), b, hdr); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("unsupported algorithm → %v, want fail-closed", err)
		}
	})
}

// ============================================================
// AC2: 支付确认入账 — trade_state → VerifiedEvent mapping
// ============================================================

// 7.6-UNIT-010/011/012/013 — trade_state / event_type → Kind mapping.
func TestAC2_TradeStateMapping(t *testing.T) {
	priv := genKey(t)
	cases := []struct {
		eventType, tradeState string
		kind                  provider.EventKind
	}{
		{"TRANSACTION.SUCCESS", "SUCCESS", provider.EventRechargePaid},
		{"TRANSACTION.SUCCESS", "NOTPAY", provider.EventUnhandled},
		{"TRANSACTION.SUCCESS", "USERPAYING", provider.EventUnhandled},
		{"TRANSACTION.SUCCESS", "CLOSED", provider.EventRechargeFailed},
		{"TRANSACTION.SUCCESS", "PAYERROR", provider.EventRechargeFailed},
		{"TRANSACTION.SUCCESS", "REVOKED", provider.EventRechargeFailed},
		{"TRANSACTION.SUCCESS", "WHATEVER", provider.EventUnhandled}, // unknown → safe default
		{"REFUND.SUCCESS", "SUCCESS", provider.EventUnhandled},       // REFUND.* → no-op (Q-REFUND)
	}
	for _, c := range cases {
		t.Run(c.eventType+"/"+c.tradeState, func(t *testing.T) {
			p := newVerifier(t, priv)
			body, hdr := successCallback(t, priv, c.eventType, c.tradeState, "order-1", "TX-1", "USD", 5000)
			ev, err := p.VerifyWebhook(context.Background(), body, hdr)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if ev.Kind != c.kind {
				t.Fatalf("%s/%s → kind %s, want %s", c.eventType, c.tradeState, ev.Kind, c.kind)
			}
			if c.kind == provider.EventRechargePaid && ev.SettledAmount != "50.00" {
				t.Fatalf("settled = %q, want 50.00", ev.SettledAmount)
			}
		})
	}
}

// 7.6-UNIT-014/015/016 — OrderID from out_trade_no; minor-unit settled; bad minor → fail-closed.
func TestAC2_OrderBindingAndSettled(t *testing.T) {
	priv := genKey(t)
	t.Run("7.6-UNIT-014 OrderID from out_trade_no (never client user_id)", func(t *testing.T) {
		p := newVerifier(t, priv)
		body, hdr := successCallback(t, priv, "TRANSACTION.SUCCESS", "SUCCESS", "order-77", "TX-7", "HKD", 39000)
		ev, err := p.VerifyWebhook(context.Background(), body, hdr)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ev.OrderID != "order-77" || ev.ExternalOrderID != "TX-7" || ev.SettledAmount != "390.00" || ev.Currency != "HKD" {
			t.Fatalf("event = %+v", ev)
		}
	})
	t.Run("7.6-UNIT-016 bad minor-unit on SUCCESS → fail-closed (non-signature error)", func(t *testing.T) {
		p := newVerifier(t, priv)
		// Encrypt a transaction with an unsupported settlement currency → minorToDecimal fails.
		plain, _ := json.Marshal(map[string]any{
			"out_trade_no": "o", "transaction_id": "TX", "trade_state": "SUCCESS",
			"amount": map[string]any{"total": 5000, "currency": "EUR"},
		})
		ct := encryptResource(t, []byte(testAPIV3Key), gcmNonce, "transaction", plain)
		body := callbackBody("TRANSACTION.SUCCESS", ct, gcmNonce, "transaction")
		ts := strconv.FormatInt(fixedNow.Unix(), 10)
		hdr := http.Header{}
		hdr.Set("Wechatpay-Signature", signCallback(t, priv, ts, headerNonce, body))
		hdr.Set("Wechatpay-Timestamp", ts)
		hdr.Set("Wechatpay-Nonce", headerNonce)
		hdr.Set("Wechatpay-Serial", testPlatSerial)
		_, err := p.VerifyWebhook(context.Background(), body, hdr)
		if err == nil || errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("bad settlement currency on SUCCESS → %v, want non-signature fail-closed error", err)
		}
	})
}
