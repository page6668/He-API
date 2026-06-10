// Story 7.7 — gateway endpoint tests. 7.7-INT-001 auto-recharge happy, 7.7-INT-002
// cross-user-binding → 422_invalid_payment_method, bad fields → 422_invalid_auto_-
// recharge, 7.7-INT-082 invoice IDOR → 404, 7.7-INT-011 list wrapping, confirm flow.
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func newHandler(fb *fakeBilling, fp *fakePayment) *BillingWriteHandler {
	return NewBillingWriteHandler(nil, fb, fp, nil)
}

func doReq(method, path, body, userID string, fn http.HandlerFunc, pathVals map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if userID != "" {
		r = r.WithContext(middleware.BearerWithUserID(r.Context(), userID))
	}
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	fn(rec, r)
	return rec
}

func TestUpdateAutoRecharge_Happy(t *testing.T) {
	h := newHandler(&fakeBilling{}, &fakePayment{})
	rec := doReq(http.MethodPut, "/v1/billing/auto-recharge",
		`{"enabled":true,"threshold_usd":"5.00","amount_usd":"20.00","payment_method_id":"m1"}`, "u1", h.UpdateAutoRecharge, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// 7.7-INT-002 — billing-svc rejects a foreign method with PermissionDenied → the
// gateway maps it to 422_invalid_payment_method (cross-user-binding guard).
func TestUpdateAutoRecharge_ForeignMethod422(t *testing.T) {
	fb := &fakeBilling{err: connect.NewError(connect.CodePermissionDenied, context.DeadlineExceeded)}
	h := newHandler(fb, &fakePayment{})
	rec := doReq(http.MethodPut, "/v1/billing/auto-recharge",
		`{"enabled":true,"threshold_usd":"5.00","amount_usd":"20.00","payment_method_id":"other-users-method"}`, "u1", h.UpdateAutoRecharge, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "422_invalid_payment_method") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUpdateAutoRecharge_BadFields422(t *testing.T) {
	fb := &fakeBilling{err: connect.NewError(connect.CodeInvalidArgument, context.DeadlineExceeded)}
	h := newHandler(fb, &fakePayment{})
	rec := doReq(http.MethodPut, "/v1/billing/auto-recharge",
		`{"enabled":true,"threshold_usd":"-1","amount_usd":"20.00","payment_method_id":"m1"}`, "u1", h.UpdateAutoRecharge, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "422_invalid_auto_recharge") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 7.7-INT-082 — invoice PDF not found (foreign id) → 404_not_found.
func TestGetInvoicePDF_NotFound404(t *testing.T) {
	h := newHandler(&fakeBilling{}, &fakePayment{}) // fake returns Found:false
	rec := doReq(http.MethodGet, "/v1/billing/invoices/inv1/pdf", "", "u1", h.GetInvoicePDF, map[string]string{"id": "inv1"})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "404_not_found") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListPaymentMethods_Wrapped(t *testing.T) {
	h := newHandler(&fakeBilling{}, &fakePayment{})
	rec := doReq(http.MethodGet, "/v1/billing/payment-methods", "", "u1", h.ListPaymentMethods, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payment_methods":[]`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// Confirm flow: server-side token retrieval → save bound to the user.
func TestConfirmPaymentMethod_SavesToken(t *testing.T) {
	h := newHandler(&fakeBilling{}, &fakePayment{})
	rec := doReq(http.MethodPost, "/v1/billing/payment-methods/confirm", `{"setup_intent_id":"seti_1"}`, "u1", h.ConfirmPaymentMethod, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"last4":"4242"`) || !strings.Contains(rec.Body.String(), `"id":"m1"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
