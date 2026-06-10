// Story 7.5 — alipay provider unit suite (implements the QA test-design skeleton,
// Turing 2026-06-10, docs/qa/assessments/7.5-test-design-20260610.md). White-box
// (package alipay) so it exercises parseNotification / decimalToMinor /
// minorToDecimal / the constructor directly. Antom is mocked with httptest; the
// asymmetric-RSA2 lanes run fully offline over an EPHEMERAL in-test RSA keypair —
// no real Alipay+ key is needed (BR-W: a public key verifies; the test holds the
// matching private key only to FORGE valid + adversarial fixtures).
//
// Cross-package scenarios (the credit applier, the PG state-machine, the gateway
// route-auth + raw-body proxy, the E2E journeys) live in their real homes — the
// credit spine is REUSED from 7.3/7.2 (proven by its suites); alipay emits the
// SAME PaymentEvent so the inherited applier credits it (re-asserted at the
// producer boundary in internal/webhook/alipay_ingress_test.go).
package alipay

import (
	"context"
	"crypto"
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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// Compile-time + runtime proof that alipay satisfies the SAME seam as
// stripe/paypal/coinbase — the recharge handler + credit.go stay provider-agnostic
// (7.5-INT-025 seam parity / proof-of-reuse for 7.6 WeChat, BR-A-1).
var _ provider.PaymentProvider = (*Provider)(nil)

const testClientID = "SANDBOX_alipay_test_client"

// fixedNow pins the provider clock so Request-Time freshness is deterministic.
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

// signWith builds a valid Antom Signature header value over the constructed string
// `POST <notifyPath>\n<clientID>.<requestTime>.<body>` using priv (Alipay+'s key).
func signWith(t *testing.T, priv *rsa.PrivateKey, notifyPath, clientID, requestTime string, body []byte) string {
	t.Helper()
	signStr := "POST " + notifyPath + "\n" + clientID + "." + requestTime + "." + string(body)
	digest := sha256.Sum256([]byte(signStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return "algorithm=RSA256,keyVersion=1,signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
}

// notifBody builds an Antom payment notification JSON body.
func notifBody(resultStatus, paymentID, orderID, currency, minorValue string) []byte {
	b, _ := json.Marshal(map[string]any{
		"result":           map[string]string{"resultStatus": resultStatus},
		"paymentId":        paymentID,
		"paymentRequestId": orderID,
		"paymentAmount":    map[string]string{"currency": currency, "value": minorValue},
	})
	return b
}

// newVerifier builds a Provider whose verify key is priv's public key, clock pinned.
func newVerifier(t *testing.T, priv *rsa.PrivateKey, opts ...Option) *Provider {
	t.Helper()
	base := []Option{WithClock(func() time.Time { return fixedNow })}
	return New(testClientID, privPEM(t, priv), pubPEM(t, &priv.PublicKey), append(base, opts...)...)
}

// validHeaders returns headers for a fresh, correctly-signed notification.
func validHeaders(t *testing.T, priv *rsa.PrivateKey, notifyPath string, body []byte) http.Header {
	t.Helper()
	rt := fixedNow.Format(time.RFC3339)
	h := http.Header{}
	h.Set("Client-Id", testClientID)
	h.Set("Request-Time", rt)
	h.Set("Signature", signWith(t, priv, notifyPath, testClientID, rt, body))
	return h
}

// ============================================================
// AC1: 充值下单 — CreateCheckout → Antom Cashier Payment
// ============================================================

func TestAC1_CreateCheckout(t *testing.T) {
	priv := genKey(t)

	// 7.5-UNIT-001/002/003 | P0 — builds the Antom POST /pay request, signs it, maps the response.
	t.Run("7.5-UNIT-001 builds + signs Antom pay request, maps response", func(t *testing.T) {
		var gotBody []byte
		var gotHeaders http.Header
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotHeaders = r.Header.Clone()
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"paymentId":"PAY-123","normalUrl":"https://cashier.antom/checkout/abc","result":{"resultStatus":"S"}}`))
		}))
		defer srv.Close()

		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		res, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "order-1", UserID: "user-1", Amount: "50.00", Currency: "USD"})
		if err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if res.CheckoutURL != "https://cashier.antom/checkout/abc" || res.ExternalOrderID != "PAY-123" {
			t.Fatalf("mapped result = %+v", res)
		}
		if gotPath != payPath {
			t.Fatalf("path = %q, want %q", gotPath, payPath)
		}
		// 7.5-UNIT-002 — outbound RSA signature header present + algorithm correct.
		sig := gotHeaders.Get("Signature")
		if !strings.HasPrefix(sig, "algorithm=RSA256,") || !strings.Contains(sig, "signature=") {
			t.Fatalf("outbound Signature header = %q", sig)
		}
		if gotHeaders.Get("Client-Id") != testClientID || gotHeaders.Get("Request-Time") == "" {
			t.Fatalf("missing Client-Id/Request-Time: %v", gotHeaders)
		}
		// 7.5-UNIT-003 — minor-unit amount on the wire ("50.00" USD → "5000").
		var sent struct {
			PaymentRequestID string            `json:"paymentRequestId"`
			PaymentAmount    map[string]string `json:"paymentAmount"`
			Metadata         map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(gotBody, &sent); err != nil {
			t.Fatalf("unmarshal sent body: %v", err)
		}
		if sent.PaymentAmount["value"] != "5000" || sent.PaymentAmount["currency"] != "USD" {
			t.Fatalf("paymentAmount = %v, want value=5000 currency=USD", sent.PaymentAmount)
		}
		if sent.PaymentRequestID != "order-1" || sent.Metadata["order_id"] != "order-1" || sent.Metadata["user_id"] != "user-1" {
			t.Fatalf("order/metadata binding wrong: req=%q meta=%v", sent.PaymentRequestID, sent.Metadata)
		}
	})

	// 7.5-INT-002/005-ish (unit-level) — CNY charge sends "35000" minor units.
	t.Run("7.5-UNIT-003b CNY charge sends 35000 minor units", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"paymentId":"PAY-9","normalUrl":"https://cashier.antom/x","result":{"resultStatus":"S"}}`))
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o2", UserID: "u2", Amount: "350.00", Currency: "CNY"}); err != nil {
			t.Fatalf("CreateCheckout CNY: %v", err)
		}
		var sent struct {
			PaymentAmount map[string]string `json:"paymentAmount"`
		}
		_ = json.Unmarshal(gotBody, &sent)
		if sent.PaymentAmount["value"] != "35000" || sent.PaymentAmount["currency"] != "CNY" {
			t.Fatalf("CNY paymentAmount = %v, want value=35000", sent.PaymentAmount)
		}
	})

	// 7.5-BLIND-ERROR-001/003/004 — Antom API failure / missing handle → wrapped error, no key leak.
	t.Run("7.5-BLIND-ERROR-001 Antom 4xx → wrapped error, no key leak", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		_, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.00", Currency: "USD"})
		if err == nil {
			t.Fatal("want error on 502")
		}
		if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), testClientID) {
			t.Fatalf("error leaks secret/url: %v", err)
		}
	})

	t.Run("7.5-BLIND-ERROR-004 2xx missing paymentId/url → contract error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"result":{"resultStatus":"S"}}`))
		}))
		defer srv.Close()
		p := newVerifier(t, priv, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o", UserID: "u", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want contract error on missing handle")
		}
	})

	// 7.5-BLIND-BOUNDARY-005 — sub-minor amount rejected before provider call.
	t.Run("7.5-BLIND-BOUNDARY-005 sub-minor amount → error, no provider call", func(t *testing.T) {
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
}

// 7.5-UNIT-004/005/008 — Name, constructor wiring, no JSON float.
func TestProviderBasics(t *testing.T) {
	priv := genKey(t)
	p := newVerifier(t, priv)
	if p.Name() != "alipay" {
		t.Fatalf("Name = %q", p.Name())
	}
	// CreateSubscription is not-supported (mirrors coinbase, Q-SUBSCOPE).
	if _, err := p.CreateSubscription(context.Background(), provider.Subscription{}); err == nil {
		t.Fatal("CreateSubscription must return not-supported")
	}
	// WithNotifyPath / WithBaseURL override.
	p2 := New(testClientID, privPEM(t, priv), pubPEM(t, &priv.PublicKey), WithNotifyPath("/custom/notify"), WithBaseURL("https://x"))
	if p2.notifyPath != "/custom/notify" || p2.baseURL != "https://x" {
		t.Fatalf("options not applied: %q %q", p2.notifyPath, p2.baseURL)
	}
	// Bad PEM → keyErr set → fail-closed on use.
	bad := New(testClientID, "not-a-key", "not-a-key")
	if bad.keyErr == nil {
		t.Fatal("expected keyErr on bad PEM")
	}
	if _, err := bad.VerifyWebhook(context.Background(), []byte("{}"), http.Header{}); !errors.Is(err, provider.ErrSignatureInvalid) {
		t.Fatalf("bad-key verify should fail closed, got %v", err)
	}
}

// 7.5-UNIT-006/007 — per-currency minor-unit table + fail-closed round-trip.
func TestMinorUnits(t *testing.T) {
	cases := []struct {
		dec, cur, minor string
		ok              bool
	}{
		{"50.00", "USD", "5000", true},
		{"350.00", "CNY", "35000", true},
		{"0.01", "USD", "1", true},
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
	// Reverse round-trip.
	for _, c := range []struct{ minor, cur, dec string }{
		{"5000", "USD", "50.00"}, {"35000", "CNY", "350.00"}, {"1", "USD", "0.01"},
	} {
		got, err := minorToDecimal(c.minor, c.cur)
		if err != nil || got != c.dec {
			t.Fatalf("minorToDecimal(%q,%q)=%q,%v want %q", c.minor, c.cur, got, err, c.dec)
		}
	}
	// Non-integer minor value → fail closed.
	if _, err := minorToDecimal("50.5", "USD"); err == nil {
		t.Fatal("non-integer minor value must fail closed")
	}
}

// ============================================================
// AC3: webhook 安全 — RSA2 asymmetric verification (security core)
// ============================================================

func TestAC3_VerifyWebhook(t *testing.T) {
	priv := genKey(t)
	body := notifBody("S", "PAY-1", "order-1", "USD", "5000")

	// 7.5-UNIT-020 | P0 — valid asymmetric verify → parse → VerifiedEvent.
	t.Run("7.5-UNIT-020 valid signature verifies + parses", func(t *testing.T) {
		p := newVerifier(t, priv)
		ev, err := p.VerifyWebhook(context.Background(), body, validHeaders(t, priv, DefaultNotifyPath, body))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ev.Kind != provider.EventRechargePaid || ev.OrderID != "order-1" || ev.ExternalOrderID != "PAY-1" || ev.SettledAmount != "50.00" || ev.Currency != "USD" {
			t.Fatalf("event = %+v", ev)
		}
	})

	// 7.5-UNIT-021/022/023 | P0 — forged / absent / wrong-key → ErrSignatureInvalid, body never parsed.
	t.Run("7.5-UNIT-021 forged signature rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		h := validHeaders(t, priv, DefaultNotifyPath, body)
		h.Set("Signature", "algorithm=RSA256,keyVersion=1,signature="+url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("garbage"))))
		if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("forged → %v, want ErrSignatureInvalid", err)
		}
	})
	t.Run("7.5-UNIT-022 absent headers rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		for _, drop := range []string{"Signature", "Client-Id", "Request-Time"} {
			h := validHeaders(t, priv, DefaultNotifyPath, body)
			h.Del(drop)
			if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
				t.Fatalf("missing %s → %v, want ErrSignatureInvalid", drop, err)
			}
		}
	})
	t.Run("7.5-UNIT-023 wrong key (attacker-signed) rejected", func(t *testing.T) {
		attacker := genKey(t)
		p := newVerifier(t, priv)                               // verifies with priv's public key
		h := validHeaders(t, attacker, DefaultNotifyPath, body) // but signed by attacker
		if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("wrong-key → %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.5-UNIT-024 | P0 — constructed-string component tampering (table) → all reject.
	t.Run("7.5-UNIT-024 constructed-string component tamper rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		rt := fixedNow.Format(time.RFC3339)
		base := func() http.Header {
			h := http.Header{}
			h.Set("Client-Id", testClientID)
			h.Set("Request-Time", rt)
			h.Set("Signature", signWith(t, priv, DefaultNotifyPath, testClientID, rt, body))
			return h
		}
		// tamper body
		h := base()
		if _, err := p.VerifyWebhook(context.Background(), notifBody("S", "PAY-1", "order-1", "USD", "9999"), h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered body must reject")
		}
		// tamper Request-Time (re-sign would be needed; altering it post-sign breaks verify)
		h = base()
		h.Set("Request-Time", fixedNow.Add(time.Minute).Format(time.RFC3339))
		if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered Request-Time must reject")
		}
		// tamper Client-Id
		h = base()
		h.Set("Client-Id", "OTHER")
		if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("tampered Client-Id must reject")
		}
	})

	// 7.5-UNIT-025 | P0 — canonicalisation source: signed over the CONFIGURED notify
	// path, NOT some other path. A provider configured with a different notify path
	// fails to verify a notification signed over the real public path.
	t.Run("7.5-UNIT-025 reconstructs from configured notify path", func(t *testing.T) {
		// signed over the PUBLIC path "/v1/billing/webhooks/alipay"
		h := validHeaders(t, priv, DefaultNotifyPath, body)
		// provider misconfigured with the INTERNAL path → must fail (proves it uses
		// the configured path, not a hardcoded/derived one).
		pWrong := newVerifier(t, priv, WithNotifyPath("/webhooks/alipay"))
		if _, err := pWrong.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatal("notify-path mismatch must reject (canonicalisation source)")
		}
		// correctly-configured provider verifies the same notification.
		pRight := newVerifier(t, priv)
		if _, err := pRight.VerifyWebhook(context.Background(), body, h); err != nil {
			t.Fatalf("configured notify path must verify: %v", err)
		}
	})

	// 7.5-UNIT-026/027 | P0 — replay layer 1: Request-Time freshness window.
	t.Run("7.5-UNIT-026 stale Request-Time rejected", func(t *testing.T) {
		p := newVerifier(t, priv, WithTolerance(5*time.Minute))
		staleRT := fixedNow.Add(-10 * time.Minute).Format(time.RFC3339)
		h := http.Header{}
		h.Set("Client-Id", testClientID)
		h.Set("Request-Time", staleRT)
		h.Set("Signature", signWith(t, priv, DefaultNotifyPath, testClientID, staleRT, body))
		if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("stale Request-Time → %v, want reject", err)
		}
	})
	t.Run("7.5-UNIT-027 just-inside window accepted", func(t *testing.T) {
		p := newVerifier(t, priv, WithTolerance(5*time.Minute))
		rt := fixedNow.Add(-4 * time.Minute).Format(time.RFC3339)
		h := http.Header{}
		h.Set("Client-Id", testClientID)
		h.Set("Request-Time", rt)
		h.Set("Signature", signWith(t, priv, DefaultNotifyPath, testClientID, rt, body))
		if _, err := p.VerifyWebhook(context.Background(), body, h); err != nil {
			t.Fatalf("in-window → %v, want accept", err)
		}
	})

	// 7.5-UNIT-028 — malformed Signature header (bad algorithm / non-b64) → reject.
	t.Run("7.5-UNIT-028 malformed Signature header rejected", func(t *testing.T) {
		p := newVerifier(t, priv)
		rt := fixedNow.Format(time.RFC3339)
		for _, sig := range []string{
			"algorithm=HS256,signature=abc",                        // wrong algorithm
			"keyVersion=1,signature=abc",                           // missing algorithm
			"algorithm=RSA256,keyVersion=1",                        // missing signature
			"algorithm=RSA256,keyVersion=1,signature=%%%notb64%%%", // non-base64
		} {
			h := http.Header{}
			h.Set("Client-Id", testClientID)
			h.Set("Request-Time", rt)
			h.Set("Signature", sig)
			if _, err := p.VerifyWebhook(context.Background(), body, h); !errors.Is(err, provider.ErrSignatureInvalid) {
				t.Fatalf("malformed sig %q → %v, want reject", sig, err)
			}
		}
	})
}

// ============================================================
// AC2: 支付确认入账 — resultStatus → VerifiedEvent mapping
// ============================================================

// 7.5-UNIT-010/011/012/013 — resultStatus → Kind mapping.
func TestAC2_ResultStatusMapping(t *testing.T) {
	priv := genKey(t)
	cases := []struct {
		status string
		kind   provider.EventKind
	}{
		{"S", provider.EventRechargePaid},
		{"U", provider.EventUnhandled},
		{"F", provider.EventRechargeFailed},
		{"X", provider.EventUnhandled}, // unknown → safe default
	}
	for _, c := range cases {
		t.Run("resultStatus="+c.status, func(t *testing.T) {
			body := notifBody(c.status, "PAY-1", "order-1", "USD", "5000")
			p := newVerifier(t, priv)
			ev, err := p.VerifyWebhook(context.Background(), body, validHeaders(t, priv, DefaultNotifyPath, body))
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if ev.Kind != c.kind {
				t.Fatalf("status %s → kind %s, want %s", c.status, ev.Kind, c.kind)
			}
			if c.kind == provider.EventRechargePaid && ev.SettledAmount != "50.00" {
				t.Fatalf("settled = %q, want 50.00", ev.SettledAmount)
			}
		})
	}
}

// 7.5-UNIT-014/015/016 — OrderID from paymentRequestId; minor-unit settled; bad minor → fail-closed.
func TestAC2_OrderBindingAndSettled(t *testing.T) {
	priv := genKey(t)
	t.Run("7.5-UNIT-014 OrderID from paymentRequestId (never client user_id)", func(t *testing.T) {
		body := notifBody("S", "PAY-7", "order-77", "CNY", "35000")
		p := newVerifier(t, priv)
		ev, err := p.VerifyWebhook(context.Background(), body, validHeaders(t, priv, DefaultNotifyPath, body))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		if ev.OrderID != "order-77" || ev.SettledAmount != "350.00" || ev.Currency != "CNY" {
			t.Fatalf("event = %+v", ev)
		}
	})
	t.Run("7.5-UNIT-016 bad minor-unit on S → fail-closed (non-signature error)", func(t *testing.T) {
		body := notifBody("S", "PAY-8", "order-8", "USD", "50.5") // fractional minor → invalid
		p := newVerifier(t, priv)
		_, err := p.VerifyWebhook(context.Background(), body, validHeaders(t, priv, DefaultNotifyPath, body))
		if err == nil || errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("bad minor on S → %v, want non-signature fail-closed error", err)
		}
	})
}
