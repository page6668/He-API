// Story 7.3 (AC1 / AC2) — the billing WRITE endpoints:
//
//	POST /v1/billing/recharge       → create a pending recharge order (billing-svc
//	                                   CreateRechargeOrder gRPC, Q-ORDEROWNER) +
//	                                   open a provider checkout (payment-svc
//	                                   CreateCheckout). Returns the checkout URL.
//	POST /v1/billing/subscriptions  → persist a subscriptions row + open a provider
//	                                   subscription (RAIL only — Q-SUBSCOPE).
//
// Both are mounted behind bearer-API-key auth (user_id from the validated key —
// the order/subscription is bound to the authenticated user SERVER-SIDE, never a
// client-asserted id, BR-R-4). Money fields are string-decimals (BR-R-5). 7.3
// enables USD only at the endpoint (Q-CURRENCY m3); a non-USD recharge is rejected
// with 400_unsupported_currency until 7.5/7.6 (the CNY→USD credit path is built +
// tested in billing-svc but unenabled here).
//
// The subscriptions row is persisted from the gateway over the shared billing
// pool, consistent with the GET /v1/balance direct-PG precedent (billing_read.go);
// its lifecycle (active/past_due/cancelled) is then owned by billing-svc's credit
// applier off payment.completed. A dedicated billing-svc CreateSubscription RPC
// (mirroring CreateRechargeOrder) is a clean follow-up, deferred to keep the 7.3
// additive-proto surface minimal.
package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

// supportedProviders is the enabled channel set. 7.3: stripe/paypal. 7.4 adds
// coinbase (USDC); Alipay+/WeChat are 7.5-7.6.
var supportedProviders = map[string]bool{"stripe": true, "paypal": true, "coinbase": true}

// supportedPlans is the §4.1 subscription plan enum (opaque in 7.3 — Q-SUBSCOPE).
var supportedPlans = map[string]bool{"free": true, "pro": true, "team": true, "enterprise": true}

// BillingWriteQuerier is the minimal pgx surface the subscription writer needs.
type BillingWriteQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// BillingWriteHandler serves POST /v1/billing/recharge + /subscriptions.
type BillingWriteHandler struct {
	logger  *slog.Logger
	billing billingv1connect.BillingServiceClient // CreateRechargeOrder (Q-ORDEROWNER)
	payment paymentv1connect.PaymentServiceClient // CreateCheckout / CreateSubscription
	db      BillingWriteQuerier                   // subscriptions row persistence
}

// NewBillingWriteHandler builds the write handler. Any of billing/payment/db may
// be nil — the corresponding endpoint then returns 503 rather than nil-panicking.
func NewBillingWriteHandler(logger *slog.Logger, billing billingv1connect.BillingServiceClient, payment paymentv1connect.PaymentServiceClient, db BillingWriteQuerier) *BillingWriteHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &BillingWriteHandler{logger: logger, billing: billing, payment: payment, db: db}
}

type rechargeRequest struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Provider string `json:"provider"`
}

type rechargeResponse struct {
	OrderID     string `json:"order_id"`
	CheckoutURL string `json:"checkout_url"`
	Provider    string `json:"provider"`
	// UsdcAddress is the USDC deposit address for the coinbase channel (Q-ADDRESS,
	// Story 7.4). It rides through CreateCheckoutResponse.client_token (no proto
	// change) and is omitted for providers that do not supply one.
	UsdcAddress string `json:"usdc_address,omitempty"`
}

// Recharge handles POST /v1/billing/recharge.
func (h *BillingWriteHandler) Recharge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}
	if h.billing == nil || h.payment == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Payment service not configured.", nil)
		return
	}

	var req rechargeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Malformed request body.", nil)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if !supportedProviders[provider] {
		p := req.Provider
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unsupported_payment_provider", "Unsupported payment provider. Supported: stripe, paypal, coinbase.", &p)
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(req.Currency))
	if currency != "USD" { // Q-CURRENCY m3 — 7.3 enables USD only
		c := req.Currency
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unsupported_currency", "Unsupported recharge currency. 7.3 supports USD only.", &c)
		return
	}
	amt, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil || !amt.IsPositive() {
		a := req.Amount
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Amount must be a positive decimal string.", &a)
		return
	}
	amountStr := amt.StringFixed(moneyScale)

	// 1) Persist the PENDING order via billing-svc (sole writer — Q-ORDEROWNER).
	orderResp, err := h.billing.CreateRechargeOrder(ctx, connect.NewRequest(&billingv1.CreateRechargeOrderRequest{
		UserId:          userID,
		Amount:          amountStr,
		Currency:        currency,
		PaymentProvider: provider,
	}))
	if err != nil {
		h.writeUpstreamErr(w, ctx, "recharge_create_order", err)
		return
	}
	orderID := orderResp.Msg.GetOrderId()

	// 2) Open the provider checkout, carrying our order id as metadata (BR-R-4).
	checkout, err := h.payment.CreateCheckout(ctx, connect.NewRequest(&paymentv1.CreateCheckoutRequest{
		OrderId:         orderID,
		UserId:          userID,
		Amount:          amountStr,
		Currency:        currency,
		PaymentProvider: provider,
	}))
	if err != nil {
		h.writeUpstreamErr(w, ctx, "recharge_create_checkout", err)
		return
	}

	writeJSON(w, http.StatusOK, rechargeResponse{
		OrderID:     orderID,
		CheckoutURL: checkout.Msg.GetCheckoutUrl(),
		Provider:    provider,
		// For coinbase the USDC deposit address rides in client_token (Q-ADDRESS);
		// empty for stripe/paypal → omitted by the json:omitempty tag.
		UsdcAddress: checkout.Msg.GetClientToken(),
	})
}

type subscriptionRequest struct {
	Plan     string `json:"plan"`
	Provider string `json:"provider"`
}

type subscriptionResponse struct {
	SubscriptionID string `json:"subscription_id"`
	CheckoutURL    string `json:"checkout_url"`
}

const (
	insertSubscriptionSQL = `INSERT INTO he_api.subscriptions
		(user_id, plan, status, payment_provider, created_at)
	VALUES ($1, $2, 'past_due', $3, NOW())
	RETURNING id::text`
	bindSubscriptionExtSQL = `UPDATE he_api.subscriptions SET external_subscription_id = $2 WHERE id = $1`
)

// Subscription handles POST /v1/billing/subscriptions (RAIL only — Q-SUBSCOPE).
func (h *BillingWriteHandler) Subscription(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}
	if h.payment == nil || h.db == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Payment service not configured.", nil)
		return
	}

	var req subscriptionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Malformed request body.", nil)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if !supportedProviders[provider] {
		p := req.Provider
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unsupported_payment_provider", "Unsupported payment provider. Supported: stripe, paypal.", &p)
		return
	}
	plan := strings.ToLower(strings.TrimSpace(req.Plan))
	if !supportedPlans[plan] {
		p := req.Plan
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Unknown plan. Supported: free, pro, team, enterprise.", &p)
		return
	}

	// 1) Persist the subscriptions row (status past_due until the active webhook).
	var subscriptionID string
	if err := h.db.QueryRow(ctx, insertSubscriptionSQL, userID, plan, provider).Scan(&subscriptionID); err != nil {
		h.logger.ErrorContext(ctx, "subscription_insert_failed", slog.String("event", "subscription_insert_failed"), slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "Unable to create subscription.", nil)
		return
	}

	// 2) Open the provider subscription.
	sub, err := h.payment.CreateSubscription(ctx, connect.NewRequest(&paymentv1.CreateSubscriptionRequest{
		SubscriptionId:  subscriptionID,
		UserId:          userID,
		Plan:            plan,
		PaymentProvider: provider,
	}))
	if err != nil {
		h.writeUpstreamErr(w, ctx, "subscription_create", err)
		return
	}

	// 3) Bind the provider subscription id back onto our row (webhook lookup key).
	if extID := sub.Msg.GetExternalSubscriptionId(); extID != "" {
		if _, err := h.db.Exec(ctx, bindSubscriptionExtSQL, subscriptionID, extID); err != nil {
			h.logger.WarnContext(ctx, "subscription_bind_ext_failed", slog.String("event", "subscription_bind_ext_failed"), slog.String("error", err.Error()))
		}
	}

	writeJSON(w, http.StatusOK, subscriptionResponse{
		SubscriptionID: subscriptionID,
		CheckoutURL:    sub.Msg.GetCheckoutUrl(),
	})
}

// writeUpstreamErr maps a downstream Connect error to the §5.1.2 envelope. A
// provider-side failure (CodeUnavailable from payment-svc) surfaces as
// 402_payment_failed (a payment-domain failure to the user — NOT a 5xx, per the
// Architect ruling: 5xx is reserved for our-own-infra faults). An InvalidArgument
// is a 400; anything else is 500.
func (h *BillingWriteHandler) writeUpstreamErr(w http.ResponseWriter, ctx context.Context, op string, err error) {
	code := connect.CodeOf(err)
	h.logger.WarnContext(ctx, "billing_write_upstream_error",
		slog.String("event", "billing_write_upstream_error"),
		slog.String("op", op),
		slog.String("connect_code", code.String()),
	)
	switch code {
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Invalid payment request.", nil)
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusPaymentRequired, "402_payment_failed", "The payment provider could not process the request.", nil)
	default:
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "Unable to process payment request.", nil)
	}
}

// decodeJSONBody reads a small JSON body, rejecting unknown fields. (writeJSON
// for the success path lives in auth.go — the package's shared success encoder.)
func decodeJSONBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
