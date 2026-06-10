// Story 7.3 (AC1) gateway recharge handler. Validates the request, calls
// billing-svc CreateRechargeOrder + payment-svc CreateCheckout, and maps
// downstream Connect errors to §5.1.2 envelopes. 7.3 enables USD only (Q-CURRENCY
// m3); provider ∈ {stripe,paypal}; amount must be a positive decimal string.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

// fakeBilling implements billingv1connect.BillingServiceClient.
type fakeBilling struct {
	orderID string
	err     error
	gotReq  *billingv1.CreateRechargeOrderRequest
}

func (f *fakeBilling) CheckBalance(context.Context, *connect.Request[billingv1.CheckBalanceRequest]) (*connect.Response[billingv1.CheckBalanceResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, nil)
}
func (f *fakeBilling) CreateRechargeOrder(_ context.Context, req *connect.Request[billingv1.CreateRechargeOrderRequest]) (*connect.Response[billingv1.CreateRechargeOrderResponse], error) {
	f.gotReq = req.Msg
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.CreateRechargeOrderResponse{OrderId: f.orderID, Status: "pending"}), nil
}

// Story 7.7 stubs (overridden per-test where exercised).
func (f *fakeBilling) SetAutoRecharge(_ context.Context, req *connect.Request[billingv1.SetAutoRechargeRequest]) (*connect.Response[billingv1.SetAutoRechargeResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.SetAutoRechargeResponse{Enabled: req.Msg.GetEnabled()}), nil
}
func (f *fakeBilling) SavePaymentMethod(context.Context, *connect.Request[billingv1.SavePaymentMethodRequest]) (*connect.Response[billingv1.SavePaymentMethodResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.SavePaymentMethodResponse{Id: "m1", IsDefault: true}), nil
}
func (f *fakeBilling) ListPaymentMethods(context.Context, *connect.Request[billingv1.ListPaymentMethodsRequest]) (*connect.Response[billingv1.ListPaymentMethodsResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.ListPaymentMethodsResponse{MethodsJson: "[]"}), nil
}
func (f *fakeBilling) DeletePaymentMethod(context.Context, *connect.Request[billingv1.DeletePaymentMethodRequest]) (*connect.Response[billingv1.DeletePaymentMethodResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.DeletePaymentMethodResponse{Deleted: true}), nil
}
func (f *fakeBilling) ListInvoices(context.Context, *connect.Request[billingv1.ListInvoicesRequest]) (*connect.Response[billingv1.ListInvoicesResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.ListInvoicesResponse{InvoicesJson: "[]"}), nil
}
func (f *fakeBilling) GetInvoicePdf(context.Context, *connect.Request[billingv1.GetInvoicePdfRequest]) (*connect.Response[billingv1.GetInvoicePdfResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.GetInvoicePdfResponse{Found: false}), nil
}

// Story 7.8 subscription-tier stubs.
func (f *fakeBilling) GetSubscription(context.Context, *connect.Request[billingv1.GetSubscriptionRequest]) (*connect.Response[billingv1.GetSubscriptionResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.GetSubscriptionResponse{Plan: "free", Status: "active"}), nil
}
func (f *fakeBilling) ChangePlan(context.Context, *connect.Request[billingv1.ChangePlanRequest]) (*connect.Response[billingv1.ChangePlanResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.ChangePlanResponse{Direction: "same", NewPlan: "free"}), nil
}
func (f *fakeBilling) GetEntitlements(context.Context, *connect.Request[billingv1.GetEntitlementsRequest]) (*connect.Response[billingv1.GetEntitlementsResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&billingv1.GetEntitlementsResponse{Plan: "free"}), nil
}

var _ billingv1connect.BillingServiceClient = (*fakeBilling)(nil)

// fakePayment implements paymentv1connect.PaymentServiceClient.
type fakePayment struct {
	checkoutURL string
	clientToken string // coinbase: the USDC deposit address rides here (Q-ADDRESS, 7.4)
	err         error
	gotCheckout *paymentv1.CreateCheckoutRequest
}

func (f *fakePayment) CreateCheckout(_ context.Context, req *connect.Request[paymentv1.CreateCheckoutRequest]) (*connect.Response[paymentv1.CreateCheckoutResponse], error) {
	f.gotCheckout = req.Msg
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&paymentv1.CreateCheckoutResponse{CheckoutUrl: f.checkoutURL, ClientToken: f.clientToken, ExternalOrderId: "pi_1"}), nil
}
func (f *fakePayment) CreateSubscription(context.Context, *connect.Request[paymentv1.CreateSubscriptionRequest]) (*connect.Response[paymentv1.CreateSubscriptionResponse], error) {
	return connect.NewResponse(&paymentv1.CreateSubscriptionResponse{}), nil
}

// Story 7.7 off-session stubs.
func (f *fakePayment) ChargeOffSession(context.Context, *connect.Request[paymentv1.ChargeOffSessionRequest]) (*connect.Response[paymentv1.ChargeOffSessionResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&paymentv1.ChargeOffSessionResponse{ExternalOrderId: "pi_off", Status: "pending"}), nil
}
func (f *fakePayment) CreateSetupIntent(context.Context, *connect.Request[paymentv1.CreateSetupIntentRequest]) (*connect.Response[paymentv1.CreateSetupIntentResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&paymentv1.CreateSetupIntentResponse{ClientSecret: "seti_secret", SetupIntentId: "seti_1"}), nil
}
func (f *fakePayment) RetrievePaymentMethod(context.Context, *connect.Request[paymentv1.RetrievePaymentMethodRequest]) (*connect.Response[paymentv1.RetrievePaymentMethodResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&paymentv1.RetrievePaymentMethodResponse{ProviderPmToken: "pm_saved", Brand: "visa", Last4: "4242"}), nil
}

// Story 7.8 subscription-update stub.
func (f *fakePayment) UpdateProviderSubscription(context.Context, *connect.Request[paymentv1.UpdateProviderSubscriptionRequest]) (*connect.Response[paymentv1.UpdateProviderSubscriptionResponse], error) {
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&paymentv1.UpdateProviderSubscriptionResponse{Status: "active"}), nil
}

var _ paymentv1connect.PaymentServiceClient = (*fakePayment)(nil)

func postRecharge(h *BillingWriteHandler, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/recharge", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(middleware.BearerWithUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	h.Recharge(rec, req)
	return rec
}

func TestRecharge_Happy(t *testing.T) {
	fb := &fakeBilling{orderID: "order-1"}
	fp := &fakePayment{checkoutURL: "https://checkout.stripe.com/x"}
	h := NewBillingWriteHandler(nil, fb, fp, nil)

	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"USD","provider":"stripe"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var resp rechargeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OrderID != "order-1" || resp.CheckoutURL == "" || resp.Provider != "stripe" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	// The order is bound to the authenticated user SERVER-SIDE (BR-R-4) and the
	// amount is normalised to 4dp string-decimal (BR-R-5).
	if fb.gotReq.GetUserId() != "user-1" || fb.gotReq.GetAmount() != "50.0000" {
		t.Errorf("billing req mismatch: %+v", fb.gotReq)
	}
	// The order id is carried to the provider as metadata (the webhook resolves it).
	if fp.gotCheckout.GetOrderId() != "order-1" {
		t.Errorf("checkout order id = %q, want order-1", fp.gotCheckout.GetOrderId())
	}
}

// 7.5-INT-002/003 — provider:"alipay" is accepted (added to supportedProviders) for
// BOTH USD and CNY (Q-CURRENCY); the pending order is created and bound to alipay +
// the authenticated user server-side (BR-A-3/A-6). Money fields string-decimal.
func TestRecharge_Alipay_Accepted(t *testing.T) {
	for _, currency := range []string{"USD", "CNY"} {
		t.Run(currency, func(t *testing.T) {
			fb := &fakeBilling{orderID: "order-ap"}
			fp := &fakePayment{checkoutURL: "https://cashier.antom/checkout/abc"}
			h := NewBillingWriteHandler(nil, fb, fp, nil)

			rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"`+currency+`","provider":"alipay"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			var resp rechargeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Provider != "alipay" || resp.CheckoutURL != "https://cashier.antom/checkout/abc" {
				t.Fatalf("unexpected response: %+v", resp)
			}
			// No usdc_address leaks into the alipay response (coinbase-only field).
			if resp.UsdcAddress != "" {
				t.Errorf("usdc_address = %q, want empty for alipay", resp.UsdcAddress)
			}
			if fb.gotReq.GetPaymentProvider() != "alipay" || fb.gotReq.GetCurrency() != currency || fb.gotReq.GetUserId() != "user-1" {
				t.Errorf("billing req mismatch: %+v", fb.gotReq)
			}
			if fp.gotCheckout.GetPaymentProvider() != "alipay" {
				t.Errorf("checkout provider = %q, want alipay", fp.gotCheckout.GetPaymentProvider())
			}
		})
	}
}

// 7.5-INT-005 — an unsupported currency for alipay (EUR) is rejected before any
// provider call (reuses 7.2 400_unsupported_currency).
func TestRecharge_Alipay_BadCurrency_400(t *testing.T) {
	h := NewBillingWriteHandler(nil, &fakeBilling{orderID: "x"}, &fakePayment{}, nil)
	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"EUR","provider":"alipay"}`)
	assertCode(t, rec, http.StatusBadRequest, "400_unsupported_currency")
}

// 7.6-INT-002/003/005 — provider:"wechat" is accepted (added to supportedProviders)
// for USD/HKD/CNY (Q-CURRENCY: 港澳=HKD, 海外华人=USD/CNY); the pending order is created
// and bound to wechat + the authenticated user server-side (BR-A-3/A-6). The
// code_url maps to checkout_url; no usdc_address leaks (coinbase-only field).
func TestRecharge_WeChat_Accepted(t *testing.T) {
	for _, currency := range []string{"USD", "HKD", "CNY"} {
		t.Run(currency, func(t *testing.T) {
			fb := &fakeBilling{orderID: "order-wx"}
			fp := &fakePayment{checkoutURL: "weixin://wxpay/bizpayurl?pr=abc123"}
			h := NewBillingWriteHandler(nil, fb, fp, nil)

			rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"`+currency+`","provider":"wechat"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			var resp rechargeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Provider != "wechat" || resp.CheckoutURL != "weixin://wxpay/bizpayurl?pr=abc123" {
				t.Fatalf("unexpected response: %+v", resp)
			}
			if resp.UsdcAddress != "" {
				t.Errorf("usdc_address = %q, want empty for wechat", resp.UsdcAddress)
			}
			if fb.gotReq.GetPaymentProvider() != "wechat" || fb.gotReq.GetCurrency() != currency || fb.gotReq.GetUserId() != "user-1" {
				t.Errorf("billing req mismatch: %+v", fb.gotReq)
			}
			if fp.gotCheckout.GetPaymentProvider() != "wechat" {
				t.Errorf("checkout provider = %q, want wechat", fp.gotCheckout.GetPaymentProvider())
			}
		})
	}
}

// 7.6-INT-005 — an unsupported currency for wechat (EUR) is rejected before any
// provider call (reuses 7.2 400_unsupported_currency).
func TestRecharge_WeChat_BadCurrency_400(t *testing.T) {
	h := NewBillingWriteHandler(nil, &fakeBilling{orderID: "x"}, &fakePayment{}, nil)
	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"EUR","provider":"wechat"}`)
	assertCode(t, rec, http.StatusBadRequest, "400_unsupported_currency")
}

// 7.4-INT-002 — provider:"coinbase" is accepted (added to supportedProviders); the
// pending order is created and the response surfaces the USDC deposit address
// (Q-ADDRESS) carried through CreateCheckoutResponse.client_token. Money fields
// stay string-decimal (BR-A-4).
func TestRecharge_Coinbase_Accepted(t *testing.T) {
	fb := &fakeBilling{orderID: "order-cb"}
	fp := &fakePayment{checkoutURL: "https://commerce.coinbase.com/charges/ABC123", clientToken: "0xUSDCdeposit"}
	h := NewBillingWriteHandler(nil, fb, fp, nil)

	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"USD","provider":"coinbase"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var resp rechargeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OrderID != "order-cb" || resp.Provider != "coinbase" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.CheckoutURL != "https://commerce.coinbase.com/charges/ABC123" {
		t.Errorf("checkout_url = %q (want hosted_url)", resp.CheckoutURL)
	}
	if resp.UsdcAddress != "0xUSDCdeposit" {
		t.Errorf("usdc_address = %q, want 0xUSDCdeposit (Q-ADDRESS)", resp.UsdcAddress)
	}
	// The order is bound to coinbase + the authenticated user server-side (BR-A-3/A-6).
	if fb.gotReq.GetPaymentProvider() != "coinbase" || fb.gotReq.GetUserId() != "user-1" {
		t.Errorf("billing req mismatch: %+v", fb.gotReq)
	}
	if fp.gotCheckout.GetPaymentProvider() != "coinbase" || fp.gotCheckout.GetOrderId() != "order-cb" {
		t.Errorf("checkout req mismatch: %+v", fp.gotCheckout)
	}
}

// A stripe recharge must NOT surface a usdc_address (omitempty — the field is
// coinbase-specific; no leak into other channels' responses).
func TestRecharge_Stripe_NoUsdcAddress(t *testing.T) {
	fp := &fakePayment{checkoutURL: "https://checkout.stripe.com/x"} // clientToken empty
	h := NewBillingWriteHandler(nil, &fakeBilling{orderID: "o1"}, fp, nil)
	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"USD","provider":"stripe"}`)
	if strings.Contains(rec.Body.String(), "usdc_address") {
		t.Errorf("stripe response leaked usdc_address: %s", rec.Body.String())
	}
}

func TestRecharge_BadProvider_400(t *testing.T) {
	h := NewBillingWriteHandler(nil, &fakeBilling{}, &fakePayment{}, nil)
	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"USD","provider":"dogecoin"}`)
	assertCode(t, rec, http.StatusBadRequest, "400_unsupported_payment_provider")
}

func TestRecharge_NonUSD_Rejected(t *testing.T) {
	// Q-CURRENCY m3 — 7.3 enables USD only.
	h := NewBillingWriteHandler(nil, &fakeBilling{}, &fakePayment{}, nil)
	rec := postRecharge(h, "user-1", `{"amount":"360.00","currency":"CNY","provider":"stripe"}`)
	assertCode(t, rec, http.StatusBadRequest, "400_unsupported_currency")
}

func TestRecharge_BadAmount_400(t *testing.T) {
	h := NewBillingWriteHandler(nil, &fakeBilling{}, &fakePayment{}, nil)
	for _, amt := range []string{"0", "-5", "abc"} {
		rec := postRecharge(h, "user-1", `{"amount":"`+amt+`","currency":"USD","provider":"stripe"}`)
		assertCode(t, rec, http.StatusBadRequest, "400_invalid_payment_request")
	}
}

func TestRecharge_ProviderError_402(t *testing.T) {
	// payment-svc reports the provider could not process → 402_payment_failed (NOT 5xx).
	fp := &fakePayment{err: connect.NewError(connect.CodeUnavailable, nil)}
	h := NewBillingWriteHandler(nil, &fakeBilling{orderID: "order-1"}, fp, nil)
	rec := postRecharge(h, "user-1", `{"amount":"50.00","currency":"USD","provider":"stripe"}`)
	assertCode(t, rec, http.StatusPaymentRequired, "402_payment_failed")
}

func TestRecharge_NoBearer_500(t *testing.T) {
	h := NewBillingWriteHandler(nil, &fakeBilling{}, &fakePayment{}, nil)
	rec := postRecharge(h, "", `{"amount":"50.00","currency":"USD","provider":"stripe"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// assertCode checks the §5.1.2 envelope status + code.
func assertCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, status, rec.Body.String())
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Error.Code != code {
		t.Fatalf("code = %q, want %q", env.Error.Code, code)
	}
}
