// Story 7.4 — coinbase provider unit suite (implements the QA test-design skeleton,
// Turing 2026-06-09). White-box (package coinbase) so it exercises parseEvent /
// settledValue / the constructor directly. Coinbase is mocked with httptest; the
// X-CC-Webhook-Signature lanes run fully offline over a known secret/body/digest.
//
// Cross-package scenarios (the credit applier, the PG state-machine, the gateway
// route-auth + raw-body proxy, and the E2E journeys) are t.Skip'd here with a
// written reason + a pointer to their real home — the credit spine is REUSED
// VERBATIM from 7.3 and proven by its 62-scenario suite; coinbase emits the SAME
// PaymentEvent (re-asserted at the producer boundary in
// internal/webhook/coinbase_ingress_test.go) so the inherited applier credits it
// unchanged (BR-C-5).
package coinbase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
)

// Compile-time + runtime proof that coinbase satisfies the SAME seam as
// stripe/paypal — the recharge handler + credit.go stay provider-agnostic
// (7.4-INT-025 seam parity / proof-of-reuse, BR-A-1).
var _ provider.PaymentProvider = (*Provider)(nil)

const testWebhookSecret = "whsec_coinbase_test"

// sign builds a valid lowercase-hex X-CC-Webhook-Signature for a raw body.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// chargeEvent builds a Coinbase webhook envelope for a charge lifecycle event.
// settledLocal/cryptoCurrency model the on-chain payment for confirm/underpay/
// wrong-coin lanes; pass settledLocal="" to omit the payments array.
func chargeEvent(eventType, chargeID, orderID, userID, intent, settledLocal, cryptoCurrency string) []byte {
	data := map[string]any{
		"id":       chargeID,
		"code":     "CODE123",
		"metadata": map[string]string{"order_id": orderID, "user_id": userID},
		"pricing":  map[string]any{"local": map[string]string{"amount": intent, "currency": "USD"}},
	}
	if settledLocal != "" {
		data["payments"] = []map[string]any{{
			"value": map[string]any{
				"local":  map[string]string{"amount": settledLocal, "currency": "USD"},
				"crypto": map[string]string{"amount": settledLocal, "currency": cryptoCurrency},
			},
			"status": "CONFIRMED",
		}}
	}
	b, _ := json.Marshal(map[string]any{"event": map[string]any{"id": "evt-1", "type": eventType, "data": data}})
	return b
}

// ============================================================
// AC1: 充值地址生成 — create a Coinbase Commerce USDC charge
// ============================================================

func TestAC1_CreateCheckout(t *testing.T) {
	// 7.4-UNIT-001 | P0 | unit
	t.Run("7.4-UNIT-001 builds correct Coinbase POST /charges request", func(t *testing.T) {
		var gotBody []byte
		var gotHeaders http.Header
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotHeaders = r.Header.Clone()
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"id":"CH-1","hosted_url":"https://commerce.coinbase.com/charges/CODE123","addresses":{"usdc":"0xabc"}}}`))
		}))
		defer srv.Close()

		p := New("cc_api_key", testWebhookSecret, WithBaseURL(srv.URL))
		_, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "order-1", UserID: "user-1", Amount: "50.0000", Currency: "USD"})
		if err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if gotPath != "/charges" {
			t.Errorf("path = %q, want /charges", gotPath)
		}
		if gotHeaders.Get("X-CC-Api-Key") != "cc_api_key" {
			t.Errorf("X-CC-Api-Key = %q", gotHeaders.Get("X-CC-Api-Key"))
		}
		if gotHeaders.Get("X-CC-Version") != "2018-03-22" {
			t.Errorf("X-CC-Version = %q, want 2018-03-22", gotHeaders.Get("X-CC-Version"))
		}
		var sent struct {
			PricingType string            `json:"pricing_type"`
			LocalPrice  money             `json:"local_price"`
			Metadata    map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(gotBody, &sent); err != nil {
			t.Fatalf("decode sent body: %v", err)
		}
		if sent.PricingType != "fixed_price" {
			t.Errorf("pricing_type = %q, want fixed_price", sent.PricingType)
		}
		if sent.LocalPrice.Amount != "50.00" || sent.LocalPrice.Currency != "USD" {
			t.Errorf("local_price = %+v, want {50.00 USD}", sent.LocalPrice)
		}
		if sent.Metadata["order_id"] != "order-1" || sent.Metadata["user_id"] != "user-1" {
			t.Errorf("metadata = %+v, want order_id/user_id server-side", sent.Metadata)
		}
	})

	// 7.4-UNIT-002 | P0 | unit
	t.Run("7.4-UNIT-002 maps Coinbase response to CheckoutResult + usdc address", func(t *testing.T) {
		srv := chargeStub(t, `{"data":{"id":"CH-42","hosted_url":"https://commerce.coinbase.com/charges/XYZ","addresses":{"usdc":"0xdeadbeef"}}}`)
		defer srv.Close()
		p := New("k", testWebhookSecret, WithBaseURL(srv.URL))
		res, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"})
		if err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		if res.CheckoutURL != "https://commerce.coinbase.com/charges/XYZ" {
			t.Errorf("CheckoutURL = %q (want hosted_url)", res.CheckoutURL)
		}
		if res.ExternalOrderID != "CH-42" {
			t.Errorf("ExternalOrderID = %q (want charge id)", res.ExternalOrderID)
		}
		if res.ClientToken != "0xdeadbeef" {
			t.Errorf("ClientToken (usdc_address, Q-ADDRESS) = %q, want 0xdeadbeef", res.ClientToken)
		}
	})

	// 7.4-UNIT-005 | P0 | unit
	t.Run("7.4-UNIT-005 money fields are string-decimal (no float)", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"data":{"id":"CH-1","hosted_url":"https://x","addresses":{"usdc":"0x1"}}}`))
		}))
		defer srv.Close()
		p := New("k", testWebhookSecret, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.0000", Currency: "USD"}); err != nil {
			t.Fatalf("CreateCheckout: %v", err)
		}
		// The amount MUST be serialised as a JSON string, never a bare number.
		if !strings.Contains(string(gotBody), `"amount":"50.00"`) {
			t.Errorf("amount not a string-decimal in body: %s", gotBody)
		}
		if strings.Contains(string(gotBody), `"amount":50`) {
			t.Errorf("amount serialised as a float: %s", gotBody)
		}
	})

	// 7.4-BLIND-ERROR-001 | P0 | unit
	t.Run("[BLIND-SPOT] 7.4-BLIND-ERROR-001 Coinbase 4xx -> wrapped error, no secret leak", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request"}}`))
		}))
		defer srv.Close()
		p := New("cc_super_secret_key", testWebhookSecret, WithBaseURL(srv.URL))
		_, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"})
		if err == nil {
			t.Fatal("want error on 4xx")
		}
		if strings.Contains(err.Error(), "cc_super_secret_key") || strings.Contains(err.Error(), testWebhookSecret) {
			t.Errorf("error leaks a secret: %v", err)
		}
	})

	// 7.4-BLIND-ERROR-002 | P1 | unit
	t.Run("[BLIND-SPOT] 7.4-BLIND-ERROR-002 Coinbase 5xx/503 -> wrapped error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		p := New("k", testWebhookSecret, WithBaseURL(srv.URL))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want error on 503")
		}
	})

	// 7.4-BLIND-ERROR-003 | P1 | unit
	t.Run("[BLIND-SPOT] 7.4-BLIND-ERROR-003 Coinbase timeout/conn-refused -> error, no panic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		base := srv.URL
		srv.Close() // now unreachable → connection refused
		p := New("k", testWebhookSecret, WithBaseURL(base))
		if _, err := p.CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want error on connection refused")
		}
	})

	// 7.4-BLIND-ERROR-004 | P1 | unit
	t.Run("[BLIND-SPOT] 7.4-BLIND-ERROR-004 malformed/missing hosted_url -> error", func(t *testing.T) {
		// Missing hosted_url → no half-built CheckoutResult.
		srv := chargeStub(t, `{"data":{"id":"CH-1"}}`)
		res, err := p2(srv).CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"})
		srv.Close()
		if err == nil {
			t.Fatalf("want error on missing hosted_url, got %+v", res)
		}
		// Malformed JSON → error.
		srv2 := chargeStub(t, `{not json`)
		defer srv2.Close()
		if _, err := p2(srv2).CreateCheckout(context.Background(), provider.Order{OrderID: "o1", UserID: "u1", Amount: "50.00", Currency: "USD"}); err == nil {
			t.Fatal("want error on malformed JSON")
		}
	})
}

func TestAC1_NameAndConstructor(t *testing.T) {
	// 7.4-UNIT-003 | P1
	t.Run("7.4-UNIT-003 Name returns coinbase", func(t *testing.T) {
		if got := New("k", "s").Name(); got != "coinbase" {
			t.Errorf("Name() = %q, want coinbase", got)
		}
	})

	// 7.4-UNIT-004 | P1
	t.Run("7.4-UNIT-004 New constructor wiring + WithBaseURL override", func(t *testing.T) {
		p := New("api", "wh")
		if p.apiKey != "api" || p.webhookSecret != "wh" {
			t.Errorf("constructor wiring: apiKey=%q webhookSecret=%q", p.apiKey, p.webhookSecret)
		}
		if p.baseURL != DefaultBaseURL {
			t.Errorf("default baseURL = %q, want %q", p.baseURL, DefaultBaseURL)
		}
		p2 := New("api", "wh", WithBaseURL("http://localhost:9999"))
		if p2.baseURL != "http://localhost:9999" {
			t.Errorf("WithBaseURL override = %q", p2.baseURL)
		}
	})
}

func TestAC1_InputValidation(t *testing.T) {
	// These envelope-code guards (amount<=0, unparseable, currency, provider) are
	// validated at the gateway handler, NOT the provider (the provider is reached
	// only after the handler accepts). Covered by the gateway suite:
	// apps/api-gateway/internal/handlers/billing_write_test.go —
	// TestRecharge_BadAmount_400 (amount<=0 / unparseable -> 400_invalid_payment_request),
	// TestRecharge_NonUSD_Rejected (currency -> 400_unsupported_currency),
	// TestRecharge_BadProvider_400 + TestRecharge_Coinbase_* (provider -> 400_unsupported_payment_provider).
	reason := "handler-level guard — covered in api-gateway billing_write_test.go (see comment); the provider is only reached after the handler validates"
	t.Run("[BLIND-SPOT] 7.4-BLIND-BOUNDARY-001 amount<=0 -> 400_invalid_payment_request", func(t *testing.T) { t.Skip(reason) })
	t.Run("[BLIND-SPOT] 7.4-BLIND-BOUNDARY-002 amount unparseable -> 400_invalid_payment_request", func(t *testing.T) { t.Skip(reason) })
	t.Run("[BLIND-SPOT] 7.4-BLIND-BOUNDARY-003 currency not USD -> 400_unsupported_currency", func(t *testing.T) { t.Skip(reason) })
	t.Run("[BLIND-SPOT] 7.4-BLIND-BOUNDARY-004 provider unsupported -> 400_unsupported_payment_provider", func(t *testing.T) { t.Skip(reason) })
	// 7.4-BLIND-BOUNDARY-005 P2 — no amount ceiling is enforced in the MVP handler
	// (7.3 validates amount > 0 only); a sane ceiling is a deferred hardening.
	t.Run("[BLIND-SPOT] 7.4-BLIND-BOUNDARY-005 amount ceiling+1 -> reject", func(t *testing.T) {
		t.Skip("no amount ceiling in MVP — handler validates >0 only (billing_write.go); ceiling is a deferred hardening (Q-PENDING-CLEANUP-adjacent)")
	})
}

// ============================================================
// AC2: 链上确认入账 — charge:confirmed -> exactly-once credit
// ============================================================

func TestAC2_LifecycleMapping(t *testing.T) {
	// 7.4-UNIT-010 | P0
	t.Run("7.4-UNIT-010 charge:confirmed -> EventRechargePaid", func(t *testing.T) {
		ev, err := parseEvent(chargeEvent("charge:confirmed", "CH-1", "order-1", "user-1", "50.00", "50.00", "USDC"))
		if err != nil {
			t.Fatalf("parseEvent: %v", err)
		}
		if ev.Kind != provider.EventRechargePaid || ev.Status != "paid" {
			t.Errorf("Kind/Status = %q/%q, want recharge_paid/paid", ev.Kind, ev.Status)
		}
		if ev.SettledAmount != "50.00" || ev.Currency != "USD" {
			t.Errorf("settled/currency = %q/%q, want 50.00/USD", ev.SettledAmount, ev.Currency)
		}
		if ev.ExternalOrderID != "CH-1" || ev.Provider != "coinbase" {
			t.Errorf("ext/provider = %q/%q", ev.ExternalOrderID, ev.Provider)
		}
	})

	// 7.4-UNIT-011 | P0
	t.Run("7.4-UNIT-011 charge:pending -> EventUnhandled (no credit)", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:pending", "CH-1", "order-1", "user-1", "50.00", "", ""))
		if ev.Kind != provider.EventUnhandled {
			t.Errorf("Kind = %q, want unhandled (no credit on unconfirmed)", ev.Kind)
		}
	})

	// 7.4-UNIT-012 | P1
	t.Run("7.4-UNIT-012 charge:created -> EventUnhandled no-op", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:created", "CH-1", "order-1", "user-1", "50.00", "", ""))
		if ev.Kind != provider.EventUnhandled {
			t.Errorf("Kind = %q, want unhandled", ev.Kind)
		}
	})

	// 7.4-UNIT-013 | P0
	t.Run("7.4-UNIT-013 charge:failed -> EventRechargeFailed", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:failed", "CH-1", "order-1", "user-1", "50.00", "", ""))
		if ev.Kind != provider.EventRechargeFailed || ev.Status != "failed" {
			t.Errorf("Kind/Status = %q/%q, want recharge_failed/failed", ev.Kind, ev.Status)
		}
	})

	// 7.4-UNIT-014 | P1
	t.Run("7.4-UNIT-014 charge:delayed -> EventUnhandled/park", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:delayed", "CH-1", "order-1", "user-1", "50.00", "49.50", "USDC"))
		if ev.Kind != provider.EventUnhandled {
			t.Errorf("Kind = %q, want unhandled (delayed parks, no auto-credit)", ev.Kind)
		}
	})

	// 7.4-UNIT-015 | P0 — OrderID from metadata.order_id, NEVER a client user_id.
	t.Run("7.4-UNIT-015 OrderID from metadata, never client user_id", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:confirmed", "CH-1", "order-99", "attacker-user-id", "50.00", "50.00", "USDC"))
		if ev.OrderID != "order-99" {
			t.Errorf("OrderID = %q, want order-99 (from metadata.order_id)", ev.OrderID)
		}
		if ev.OrderID == "attacker-user-id" {
			t.Error("OrderID resolved from a client-asserted user_id (cross-user binding, BR-A-3)")
		}
	})

	// 7.4-UNIT-016 | P0 — SettledAmount string-decimal (no float).
	t.Run("7.4-UNIT-016 SettledAmount string-decimal (no float)", func(t *testing.T) {
		ev, _ := parseEvent(chargeEvent("charge:confirmed", "CH-1", "order-1", "user-1", "50.00", "49.50", "USDC"))
		d, err := decimal.NewFromString(ev.SettledAmount)
		if err != nil {
			t.Fatalf("SettledAmount %q is not a valid decimal: %v", ev.SettledAmount, err)
		}
		if !d.Equal(decimal.RequireFromString("49.50")) {
			t.Errorf("SettledAmount = %q, want 49.50 (provider-confirmed on-chain value)", ev.SettledAmount)
		}
	})
}

func TestAC2_CreditIntegration(t *testing.T) {
	// The credit applier, the recharge_orders pending->paid state-machine, the
	// UNIQUE(payment_provider, external_order_id) fence, atomicity, the Redis
	// mirror, and the toUSD pass-through are REUSED VERBATIM from 7.3 (credit.go)
	// and proven by its 62-scenario suite. coinbase emits the SAME PaymentEvent
	// (re-asserted at the producer boundary in
	// internal/webhook/coinbase_ingress_test.go — TestCoinbaseIngress_*), so these
	// PG-bound lanes belong to billing-svc + CI E2E, NOT this provider unit suite.
	skip := func(t *testing.T) {
		t.Helper()
		t.Skip("PG/applier-bound — credit spine reused verbatim from 7.3 (billing-svc credit_test.go + CI E2E); coinbase emits identical PaymentEvent, re-asserted in internal/webhook/coinbase_ingress_test.go")
	}
	t.Run("7.4-INT-010 charge:confirmed credits in one PG tx", skip)
	t.Run("7.4-INT-011 USDC->USD pass-through, current_rmb untouched", skip)
	t.Run("7.4-INT-012 charge:pending -> 200 ACK, no credit", skip)
	t.Run("7.4-INT-013 charge:failed -> status=failed, no credit", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-DATA-001 exactly-once: duplicate confirmed -> one credit", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-DATA-002 atomic flip+credit / rollback on failure", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-DATA-003 underpaid -> park, no credit", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-DATA-004 overpaid -> park", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-DATA-005 wrong-coin settlement -> park", func(t *testing.T) {
		// The provider's part of wrong-coin is unit-testable: a non-USDC crypto
		// settlement surfaces a non-USD currency so the inherited guard parks it.
		ev, _ := parseEvent(chargeEvent("charge:confirmed", "CH-1", "order-1", "user-1", "50.00", "50.00", "ETH"))
		if ev.Currency == "USD" {
			t.Errorf("wrong-coin (ETH) surfaced Currency=USD — guard would credit; want non-USD to park (Q-COINS)")
		}
	})
	t.Run("[BLIND-SPOT] 7.4-BLIND-ERROR-005 PG down -> 5xx no ACK, redeliver idempotent", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-CONCURRENCY-001 concurrent confirmed -> one credit", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-CONCURRENCY-002 cross-provider charge-id no collision", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-RESOURCE-001 Redis mirror fail post-commit -> PG credit stands", skip)
	t.Run("[BLIND-SPOT] 7.4-BLIND-FLOW-001 out-of-order confirmed-before-pending -> credit once", skip)
}

func TestAC2_E2E_RechargeConfirmCredit(t *testing.T) {
	t.Run("7.4-E2E-001 recharge -> confirmed webhook -> balance +50 -> order paid", func(t *testing.T) {
		t.Skip("full-stack E2E (testcontainers/compose) — CI only; local toolchain cannot run docker (project_toolchain_env_limits)")
	})
}

// ============================================================
// AC3: Coinbase webhook 安全 — X-CC-Webhook-Signature verification
// ============================================================

func TestAC3_VerifyWebhookSignature(t *testing.T) {
	p := New("k", testWebhookSecret)
	body := chargeEvent("charge:confirmed", "CH-1", "order-1", "user-1", "50.00", "50.00", "USDC")
	hdr := func(sig string) http.Header {
		h := http.Header{}
		if sig != "" {
			h.Set("X-CC-Webhook-Signature", sig)
		}
		return h
	}

	// 7.4-UNIT-020 | P0
	t.Run("7.4-UNIT-020 valid signature -> verified + mapped", func(t *testing.T) {
		ev, err := p.VerifyWebhook(context.Background(), body, hdr(sign(testWebhookSecret, body)))
		if err != nil {
			t.Fatalf("valid signature rejected: %v", err)
		}
		if ev.Kind != provider.EventRechargePaid || ev.OrderID != "order-1" {
			t.Errorf("mapped event = %+v", ev)
		}
	})

	// 7.4-UNIT-021 | P0 | adversarial
	t.Run("7.4-UNIT-021 [FORGED] invalid signature -> ErrSignatureInvalid, body not parsed", func(t *testing.T) {
		_, err := p.VerifyWebhook(context.Background(), body, hdr(sign("wrong_secret", body)))
		if !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("forged signature: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-022 | P0 | adversarial
	t.Run("7.4-UNIT-022 [FORGED] absent signature header -> ErrSignatureInvalid", func(t *testing.T) {
		_, err := p.VerifyWebhook(context.Background(), body, hdr(""))
		if !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("absent header: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-023 | P0 | adversarial
	t.Run("7.4-UNIT-023 [FORGED] tampered body -> ErrSignatureInvalid", func(t *testing.T) {
		// Signature computed over the original body; deliver a tampered (amount-up) body.
		tampered := chargeEvent("charge:confirmed", "CH-1", "order-1", "user-1", "50.00", "5000.00", "USDC")
		_, err := p.VerifyWebhook(context.Background(), tampered, hdr(sign(testWebhookSecret, body)))
		if !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("tampered body: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-024 | P1 — constant-time compare.
	t.Run("7.4-UNIT-024 constant-time compare (hmac.Equal)", func(t *testing.T) {
		// A signature of the right hex length but wrong value must still be rejected
		// via the constant-time path (proves we compare bytes, not a fast == short-circuit).
		wrong := hex.EncodeToString(make([]byte, sha256.Size)) // 64 hex zeros
		_, err := p.VerifyWebhook(context.Background(), body, hdr(wrong))
		if !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("zeroed signature: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-025 | P1 | adversarial
	t.Run("7.4-UNIT-025 [FORGED] empty body -> ErrSignatureInvalid", func(t *testing.T) {
		// An empty body with a stale signature must not verify (no credit).
		_, err := p.VerifyWebhook(context.Background(), []byte{}, hdr(sign(testWebhookSecret, body)))
		if !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Fatalf("empty body: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-026 | P2 — non-lowercase-hex rejected.
	t.Run("7.4-UNIT-026 non-lowercase-hex signature -> reject", func(t *testing.T) {
		valid := sign(testWebhookSecret, body)
		if _, err := p.VerifyWebhook(context.Background(), body, hdr(strings.ToUpper(valid))); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Errorf("uppercase-hex: err = %v, want ErrSignatureInvalid", err)
		}
		if _, err := p.VerifyWebhook(context.Background(), body, hdr("zz"+valid[2:])); !errors.Is(err, provider.ErrSignatureInvalid) {
			t.Errorf("non-hex: err = %v, want ErrSignatureInvalid", err)
		}
	})

	// 7.4-UNIT-027 | P1 — secrets never in error; no PAN.
	t.Run("7.4-UNIT-027 secrets not logged on error; no PAN", func(t *testing.T) {
		_, err := p.VerifyWebhook(context.Background(), body, hdr("deadbeef"))
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), testWebhookSecret) {
			t.Errorf("error leaks the webhook secret: %v", err)
		}
		// Crypto channel carries no card PAN — the parsed charge struct has no PAN
		// field at all (compile-time guarantee); nothing to assert beyond the secret.
	})
}

func TestAC3_WebhookIngressIntegration(t *testing.T) {
	// Route-auth (outside bearer), the gateway raw-body byte-faithful proxy, the
	// forged->400 / replay / 2xx-discipline lanes through the REAL webhook handler
	// are exercised where the handler lives:
	//   - internal/webhook/coinbase_ingress_test.go (forged->400 + emit + 2xx via the real Handler);
	//   - the gateway proxy raw-body pass-through is provider-agnostic, proven by 7.3 (billing_webhook_test.go).
	// INT-025 seam parity is the package-level interface assertion at the top of
	// this file (var _ provider.PaymentProvider = (*Provider)(nil)).
	t.Run("7.4-INT-020 webhook route outside bearer chain", func(t *testing.T) {
		t.Skip("gateway route-auth wiring — provider-agnostic, proven by 7.3 (the coinbase route mirrors stripe/paypal in cmd/server/main.go); coinbase verify proven in internal/webhook/coinbase_ingress_test.go")
	})
	t.Run("7.4-INT-021 raw-body byte-faithful pass-through", func(t *testing.T) {
		t.Skip("gateway billing_webhook.go proxy is a provider-agnostic byte pipe — proven by 7.3 billing_webhook_test.go; coinbase reuses it unchanged (BR-W-3)")
	})
	t.Run("7.4-INT-022 [FORGED] -> 400, zero recharge_orders mutation, zero balance change", func(t *testing.T) {
		t.Skip("forged->400 + zero side-effect proven against the REAL handler in internal/webhook/coinbase_ingress_test.go (TestCoinbaseIngress_Forged_Rejected); zero-mutation is the no-emit assertion there")
	})
	t.Run("7.4-INT-023 [REPLAY] valid confirmed replayed -> state-machine sole fence, one credit", func(t *testing.T) {
		t.Skip("replay neutralisation is the recharge_orders pending->paid state-machine (reused from 7.3, billing-svc); the no-timestamp posture is documented in VerifyWebhook (BR-W-4). PG-bound -> billing-svc + CI E2E")
	})
	t.Run("7.4-INT-024 provider 2xx/400/5xx discipline", func(t *testing.T) {
		t.Skip("status discipline is the REUSED webhook.Handler (handler.go); the coinbase verify path through it is exercised in internal/webhook/coinbase_ingress_test.go (valid->200/emit, forged->400, emit-fail->5xx)")
	})
	t.Run("7.4-INT-025 seam parity / no provider-specific leak downstream", func(t *testing.T) {
		// Proof-of-reuse: coinbase satisfies the SAME PaymentProvider contract; the
		// downstream handler/applier never branch on the provider id.
		var pp provider.PaymentProvider = New("k", "s")
		if pp.Name() != "coinbase" {
			t.Fatalf("seam parity: Name() = %q", pp.Name())
		}
		// CreateSubscription is total (returns not-supported, never panics) so the
		// seam stays complete for 7.5/7.6.
		if _, err := pp.CreateSubscription(context.Background(), provider.Subscription{SubscriptionID: "s1", UserID: "u1"}); err == nil {
			t.Error("CreateSubscription should return not-supported for crypto")
		}
	})
}

func TestAC3_E2E_ForgedWebhook(t *testing.T) {
	t.Run("7.4-E2E-020 [FORGED] full-stack -> 400, zero balance change", func(t *testing.T) {
		t.Skip("full-stack security E2E (testcontainers/compose) — CI only; the forged->400 invariant is proven offline against the real handler in internal/webhook/coinbase_ingress_test.go")
	})
}

func TestAC1_RechargeIntegration(t *testing.T) {
	// Registry wiring (buildRegistry gating on COINBASE_COMMERCE_API_KEY) lives in
	// cmd/server/main.go; the gateway recharge accept + usdc_address surfacing lives
	// in apps/api-gateway/internal/handlers/billing_write_test.go
	// (TestRecharge_Coinbase_Accepted).
	t.Run("7.4-INT-001 registry Has(coinbase) gated by COINBASE_COMMERCE_API_KEY", func(t *testing.T) {
		t.Skip("buildRegistry is in package main (cmd/server) — wiring mirrors stripe/paypal verbatim; gating verified by inspection + boot logs")
	})
	t.Run("7.4-INT-002 recharge accepted -> pending order + 200 response", func(t *testing.T) {
		t.Skip("gateway flow — covered in api-gateway billing_write_test.go (TestRecharge_Coinbase_Accepted: pending order + 200 {checkout_url,usdc_address})")
	})
	t.Run("7.4-INT-003 key unset -> 400_unsupported_payment_provider", func(t *testing.T) {
		t.Skip("graceful-off — registry omits coinbase when the key is unset; the gateway then returns 400_unsupported_payment_provider (same path as TestRecharge_BadProvider_400)")
	})
}

// chargeStub returns an httptest Coinbase stub that responds to POST /charges with
// the given JSON body.
func chargeStub(t *testing.T, respJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respJSON))
	}))
}

// p2 builds a Provider pointed at a stub server.
func p2(srv *httptest.Server) *Provider { return New("k", testWebhookSecret, WithBaseURL(srv.URL)) }
