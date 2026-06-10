// Story 7.7 — the auto-recharge / saved-payment-method / invoice WRITE+READ
// endpoints, mounted behind bearer-API-key auth (user_id resolved SERVER-SIDE from
// the validated key — every operation is bound to the authenticated user, never a
// client-asserted id). All proxy to billing-svc (single-writer of the
// auto_recharge_* config + payment_methods + invoices) / payment-svc (the Stripe
// SetupIntent + off-session seam). Money fields are string-decimals (Q-Spec-4).
//
//	PUT    /v1/billing/auto-recharge            → billing.SetAutoRecharge (BR-R-7 guard)
//	POST   /v1/billing/payment-methods          → payment.CreateSetupIntent (begin)
//	POST   /v1/billing/payment-methods/confirm  → payment.RetrievePaymentMethod → billing.SavePaymentMethod
//	GET    /v1/billing/payment-methods          → billing.ListPaymentMethods (display-safe)
//	DELETE /v1/billing/payment-methods/{id}     → billing.DeletePaymentMethod (revoke cascade; IDOR→404)
//	GET    /v1/billing/invoices                 → billing.ListInvoices
//	GET    /v1/billing/invoices/{id}/pdf        → billing.GetInvoicePdf (owner-only stream; IDOR→404)
package handlers

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

type autoRechargeRequest struct {
	Enabled         bool   `json:"enabled"`
	ThresholdUSD    string `json:"threshold_usd"`
	AmountUSD       string `json:"amount_usd"`
	PaymentMethodID string `json:"payment_method_id"`
}

// UpdateAutoRecharge handles PUT /v1/billing/auto-recharge.
func (h *BillingWriteHandler) UpdateAutoRecharge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	var req autoRechargeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Malformed request body.", nil)
		return
	}
	resp, err := h.billing.SetAutoRecharge(ctx, connect.NewRequest(&billingv1.SetAutoRechargeRequest{
		UserId:          userID,
		Enabled:         req.Enabled,
		ThresholdUsd:    strings.TrimSpace(req.ThresholdUSD),
		AmountUsd:       strings.TrimSpace(req.AmountUSD),
		PaymentMethodId: strings.TrimSpace(req.PaymentMethodID),
	}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": resp.Msg.GetEnabled()})
}

// CreatePaymentMethod handles POST /v1/billing/payment-methods — begins a Stripe
// SetupIntent so the client can confirm a card off-session (the PAN never reaches
// He-API, PCI §8.4).
func (h *BillingWriteHandler) CreatePaymentMethod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.payment == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Payment service not configured.", nil)
		return
	}
	resp, err := h.payment.CreateSetupIntent(ctx, connect.NewRequest(&paymentv1.CreateSetupIntentRequest{UserId: userID}))
	if err != nil {
		h.writeUpstreamErr(w, ctx, "payment_method_setup_intent", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"client_secret":   resp.Msg.GetClientSecret(),
		"setup_intent_id": resp.Msg.GetSetupIntentId(),
	})
}

type confirmPaymentMethodRequest struct {
	SetupIntentID string `json:"setup_intent_id"`
}

// ConfirmPaymentMethod handles POST /v1/billing/payment-methods/confirm — fetches
// the confirmed token SERVER-SIDE off the SetupIntent (never client-asserted) and
// saves it bound to the user.
func (h *BillingWriteHandler) ConfirmPaymentMethod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.payment == nil || h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Payment service not configured.", nil)
		return
	}
	var req confirmPaymentMethodRequest
	if err := decodeJSONBody(r, &req); err != nil || strings.TrimSpace(req.SetupIntentID) == "" {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "A setup_intent_id is required.", nil)
		return
	}
	pm, err := h.payment.RetrievePaymentMethod(ctx, connect.NewRequest(&paymentv1.RetrievePaymentMethodRequest{
		SetupIntentId: strings.TrimSpace(req.SetupIntentID),
	}))
	if err != nil {
		h.writeUpstreamErr(w, ctx, "payment_method_retrieve", err)
		return
	}
	saved, err := h.billing.SavePaymentMethod(ctx, connect.NewRequest(&billingv1.SavePaymentMethodRequest{
		UserId:          userID,
		PaymentProvider: "stripe",
		ProviderPmToken: pm.Msg.GetProviderPmToken(),
		Brand:           pm.Msg.GetBrand(),
		Last4:           pm.Msg.GetLast4(),
	}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         saved.Msg.GetId(),
		"is_default": saved.Msg.GetIsDefault(),
		"brand":      pm.Msg.GetBrand(),
		"last4":      pm.Msg.GetLast4(),
	})
}

// ListPaymentMethods handles GET /v1/billing/payment-methods (display-safe).
func (h *BillingWriteHandler) ListPaymentMethods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	resp, err := h.billing.ListPaymentMethods(ctx, connect.NewRequest(&billingv1.ListPaymentMethodsRequest{UserId: userID}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	writeRawJSON(w, http.StatusOK, "payment_methods", resp.Msg.GetMethodsJson())
}

// DeletePaymentMethod handles DELETE /v1/billing/payment-methods/{id} (revoke
// cascade; a foreign id → 404, IDOR guard).
func (h *BillingWriteHandler) DeletePaymentMethod(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	resp, err := h.billing.DeletePaymentMethod(ctx, connect.NewRequest(&billingv1.DeletePaymentMethodRequest{
		UserId:          userID,
		PaymentMethodId: r.PathValue("id"),
	}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"deleted":                resp.Msg.GetDeleted(),
		"auto_recharge_disabled": resp.Msg.GetAutoRechargeDisabled(),
	})
}

// ListInvoices handles GET /v1/billing/invoices.
func (h *BillingWriteHandler) ListInvoices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	resp, err := h.billing.ListInvoices(ctx, connect.NewRequest(&billingv1.ListInvoicesRequest{UserId: userID}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	writeRawJSON(w, http.StatusOK, "invoices", resp.Msg.GetInvoicesJson())
}

// GetInvoicePDF handles GET /v1/billing/invoices/{id}/pdf — owner-only proxy-stream
// of the stored PDF (a foreign / missing id → 404, no existence disclosure).
func (h *BillingWriteHandler) GetInvoicePDF(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := h.requireUser(w, ctx)
	if !ok {
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	resp, err := h.billing.GetInvoicePdf(ctx, connect.NewRequest(&billingv1.GetInvoicePdfRequest{
		UserId:    userID,
		InvoiceId: r.PathValue("id"),
	}))
	if err != nil {
		h.writeBillingConfigErr(w, ctx, err)
		return
	}
	if !resp.Msg.GetFound() || len(resp.Msg.GetPdf()) == 0 {
		_ = openaierr.Write(w, ctx, http.StatusNotFound, "404_not_found", "Invoice not found.", nil)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	filename := resp.Msg.GetFilename()
	if filename == "" {
		filename = "invoice.pdf"
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp.Msg.GetPdf())
}

// requireUser resolves the bearer user id or writes the misconfig error.
func (h *BillingWriteHandler) requireUser(w http.ResponseWriter, ctx context.Context) (string, bool) {
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return "", false
	}
	return userID, true
}

// writeBillingConfigErr maps a billing-svc Connect error to the §5.1.2 envelope:
// InvalidArgument→422_invalid_auto_recharge (bad money fields), PermissionDenied→
// 422_invalid_payment_method (cross-user-binding guard), NotFound→404_not_found
// (IDOR — 404 not 403), Unavailable→503, else 500.
func (h *BillingWriteHandler) writeBillingConfigErr(w http.ResponseWriter, ctx context.Context, err error) {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusUnprocessableEntity, "422_invalid_auto_recharge", "Invalid auto-recharge configuration.", nil)
	case connect.CodePermissionDenied:
		_ = openaierr.Write(w, ctx, http.StatusUnprocessableEntity, "422_invalid_payment_method", "Selected payment method is invalid.", nil)
	case connect.CodeNotFound:
		_ = openaierr.Write(w, ctx, http.StatusNotFound, "404_not_found", "Not found.", nil)
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service unavailable.", nil)
	default:
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "Unable to process the request.", nil)
	}
}

// writeRawJSON wraps a billing-svc JSON-array payload under `key` without
// re-parsing it (the array is already display-safe, string-decimal JSON).
func writeRawJSON(w http.ResponseWriter, status int, key, arrayJSON string) {
	if strings.TrimSpace(arrayJSON) == "" {
		arrayJSON = "[]"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"` + key + `":` + arrayJSON + `}`))
}
