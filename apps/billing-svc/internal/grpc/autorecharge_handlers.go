// Story 7.7 — BillingService auto-recharge config + saved-payment-method RPCs.
// billing-svc is the single-writer of balances.auto_recharge_* and payment_methods.
// The cross-user-binding guard (BR-R-7) lives here: a payment_method_id is
// accepted ONLY if it resolves for the authenticated user.
package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/billing-svc/internal/invoice"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

func slogErr(err error) slog.Attr { return slog.String("error", err.Error()) }

// redactErr returns a token-free generic error for logging the save path — the
// stored PaymentMethod token is secret-grade and must never reach logs.
func redactErr(error) error { return constErr("payment method save failed (redacted)") }

const (
	// Enabled config upsert (balances may not exist yet — lazy-created on debit).
	setAutoRechargeOnSQL = `INSERT INTO he_api.balances
		(user_id, auto_recharge_enabled, auto_recharge_threshold_usd, auto_recharge_amount_usd, auto_recharge_payment_method_id, updated_at)
	VALUES ($1, TRUE, $2::numeric, $3::numeric, $4::uuid, NOW())
	ON CONFLICT (user_id) DO UPDATE SET
		auto_recharge_enabled = TRUE,
		auto_recharge_threshold_usd = EXCLUDED.auto_recharge_threshold_usd,
		auto_recharge_amount_usd = EXCLUDED.auto_recharge_amount_usd,
		auto_recharge_payment_method_id = EXCLUDED.auto_recharge_payment_method_id,
		updated_at = NOW()`

	// Disable keeps the saved threshold/amount/method for a later re-enable (UNIT-004).
	setAutoRechargeOffSQL = `INSERT INTO he_api.balances (user_id, auto_recharge_enabled, updated_at)
	VALUES ($1, FALSE, NOW())
	ON CONFLICT (user_id) DO UPDATE SET auto_recharge_enabled = FALSE, updated_at = NOW()`
)

// SetAutoRecharge configures balances.auto_recharge_*. When enabling, the amounts
// must be positive string-decimals (Q-Spec-4) and the payment_method_id must be a
// Stripe-card method OWNED by the user (cross-user-binding guard, BR-R-7).
func (s *Server) SetAutoRecharge(
	ctx context.Context,
	req *connect.Request[billingv1.SetAutoRechargeRequest],
) (*connect.Response[billingv1.SetAutoRechargeResponse], error) {
	if s.full == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	m := req.Msg
	userID := strings.TrimSpace(m.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidUserID)
	}

	if !m.GetEnabled() {
		if _, err := s.full.Exec(ctx, setAutoRechargeOffSQL, userID); err != nil {
			s.logger.ErrorContext(ctx, "billing_set_auto_recharge_failed", slogErr(err))
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return connect.NewResponse(&billingv1.SetAutoRechargeResponse{Enabled: false}), nil
	}

	// Enabling — validate the money fields (string-decimal, > 0).
	thr, terr := decimal.NewFromString(strings.TrimSpace(m.GetThresholdUsd()))
	if terr != nil || !thr.IsPositive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidThreshold)
	}
	amt, aerr := decimal.NewFromString(strings.TrimSpace(m.GetAmountUsd()))
	if aerr != nil || !amt.IsPositive() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidAmount)
	}

	// Cross-user-binding guard: the method must resolve for THIS user and be a
	// Stripe-card method (off-session is Stripe-only in 7.7, BR-R-4).
	pmID := strings.TrimSpace(m.GetPaymentMethodId())
	if pmID == "" || s.pm == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errInvalidPaymentMethod)
	}
	provider, found, err := s.pm.OwnedProvider(ctx, userID, pmID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !found || provider != "stripe" {
		return nil, connect.NewError(connect.CodePermissionDenied, errInvalidPaymentMethod)
	}

	if _, err := s.full.Exec(ctx, setAutoRechargeOnSQL, userID, thr.StringFixed(2), amt.StringFixed(2), pmID); err != nil {
		s.logger.ErrorContext(ctx, "billing_set_auto_recharge_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&billingv1.SetAutoRechargeResponse{Enabled: true}), nil
}

// SavePaymentMethod persists a stored off-session token bound to the user. The
// gateway supplies the token after a server-side SetupIntent retrieval (the PAN
// never touches He-API — PCI §8.4).
func (s *Server) SavePaymentMethod(
	ctx context.Context,
	req *connect.Request[billingv1.SavePaymentMethodRequest],
) (*connect.Response[billingv1.SavePaymentMethodResponse], error) {
	if s.pm == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	m := req.Msg
	id, isDefault, err := s.pm.Save(ctx, m.GetUserId(), m.GetPaymentProvider(), m.GetProviderPmToken(), m.GetBrand(), m.GetLast4())
	if err != nil {
		// Never log the token/error verbatim with the secret (BR — token discipline).
		s.logger.ErrorContext(ctx, "billing_save_payment_method_failed", slogErr(redactErr(err)))
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidPaymentMethod)
	}
	return connect.NewResponse(&billingv1.SavePaymentMethodResponse{Id: id, IsDefault: isDefault}), nil
}

// ListPaymentMethods returns the user's DISPLAY-SAFE methods as a JSON array
// (brand/last4/is_default only — never the token).
func (s *Server) ListPaymentMethods(
	ctx context.Context,
	req *connect.Request[billingv1.ListPaymentMethodsRequest],
) (*connect.Response[billingv1.ListPaymentMethodsResponse], error) {
	if s.pm == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	methods, err := s.pm.List(ctx, req.Msg.GetUserId())
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_list_payment_methods_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b, err := json.Marshal(methods)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&billingv1.ListPaymentMethodsResponse{MethodsJson: string(b)}), nil
}

// DeletePaymentMethod revokes an owned method (IDOR-guarded → CodeNotFound for a
// foreign id, which the gateway maps to 404) and reports the cascade.
func (s *Server) DeletePaymentMethod(
	ctx context.Context,
	req *connect.Request[billingv1.DeletePaymentMethodRequest],
) (*connect.Response[billingv1.DeletePaymentMethodResponse], error) {
	if s.pm == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	m := req.Msg
	deleted, disabled, err := s.pm.Delete(ctx, m.GetUserId(), m.GetPaymentMethodId())
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_delete_payment_method_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !deleted {
		return nil, connect.NewError(connect.CodeNotFound, errPaymentMethodNotFound)
	}
	return connect.NewResponse(&billingv1.DeletePaymentMethodResponse{
		Deleted:              true,
		AutoRechargeDisabled: disabled,
	}), nil
}

// ListInvoices returns the user's invoices as a JSON array (string-decimal money).
func (s *Server) ListInvoices(
	ctx context.Context,
	req *connect.Request[billingv1.ListInvoicesRequest],
) (*connect.Response[billingv1.ListInvoicesResponse], error) {
	if s.invoices == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	list, err := s.invoices.List(ctx, req.Msg.GetUserId())
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_list_invoices_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&billingv1.ListInvoicesResponse{InvoicesJson: string(b)}), nil
}

// GetInvoicePdf proxy-streams an invoice PDF OWNER-ONLY. A foreign / missing id →
// CodeNotFound (the gateway returns 404 — no existence disclosure, BR-I-6).
func (s *Server) GetInvoicePdf(
	ctx context.Context,
	req *connect.Request[billingv1.GetInvoicePdfRequest],
) (*connect.Response[billingv1.GetInvoicePdfResponse], error) {
	if s.invoices == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoConfigDB)
	}
	m := req.Msg
	owned, err := s.invoices.GetForOwner(ctx, m.GetUserId(), m.GetInvoiceId())
	if errors.Is(err, invoice.ErrNotFound) {
		// IDOR guard: 404, never 403 — do not disclose existence.
		return nil, connect.NewError(connect.CodeNotFound, errInvoiceNotFound)
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_get_invoice_pdf_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if owned.PdfObjectKey == "" || s.uploader == nil {
		// Rendered-but-not-yet-stored, or no object store wired.
		return connect.NewResponse(&billingv1.GetInvoicePdfResponse{Found: false}), nil
	}
	rc, err := s.uploader.GetObject(ctx, owned.PdfObjectKey)
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_get_invoice_pdf_object_failed", slogErr(err))
		return nil, connect.NewError(connect.CodeInternal, errInvoicePdfUnreadable)
	}
	defer func() { _ = rc.Close() }()
	pdf, err := io.ReadAll(io.LimitReader(rc, 16<<20))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errInvoicePdfUnreadable)
	}
	return connect.NewResponse(&billingv1.GetInvoicePdfResponse{
		Pdf:      pdf,
		Filename: "invoice-" + owned.Period + ".pdf",
		Found:    true,
	}), nil
}

const (
	errNoConfigDB            = constErr("billing config store not configured")
	errInvoiceNotFound       = constErr("invoice not found")
	errInvoicePdfUnreadable  = constErr("invoice pdf unreadable")
	errInvalidThreshold      = constErr("invalid_auto_recharge: threshold_usd must be a positive amount")
	errInvalidAmount         = constErr("invalid_auto_recharge: amount_usd must be a positive amount")
	errInvalidPaymentMethod  = constErr("invalid_payment_method: selected payment method is invalid")
	errPaymentMethodNotFound = constErr("payment method not found")
)
