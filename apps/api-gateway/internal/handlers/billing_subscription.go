// Story 7.8 — subscription-tier REST surface (AC1). The SINGULAR
// /v1/billing/subscription is the authenticated user's singleton management
// resource (Architect Medium-2): GET resolves the current plan + entitlements,
// PUT upgrades/downgrades, DELETE cancels-to-free-at-period-end. Distinct from
// the 7.3 plural POST /v1/billing/subscriptions create-rail (documented in
// rest-api-spec §5.1.3). GET /v1/billing/plans renders the public tier catalogue.
//
// user_id is ALWAYS server-resolved from bearer-auth (BR-S-7 — there is no
// target-user parameter; cross-user mutation is impossible). Money fields are
// string-decimals (Q-Spec-4). Unknown plan → the REUSED 7.3
// 400_invalid_payment_request (Medium-1, NOT a new 400_invalid_plan).
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1/billingv1connect"
)

// SubscriptionHandler serves the /v1/billing/subscription singleton + the public
// /v1/billing/plans catalogue over the billing-svc Connect client.
type SubscriptionHandler struct {
	logger    *slog.Logger
	billing   billingv1connect.BillingServiceClient
	catalogue plancatalogue.Catalogue
}

// NewSubscriptionHandler builds the handler. billing may be nil (the
// subscription endpoints then return 503; /v1/billing/plans still works — it
// renders the in-process catalogue with no RPC).
func NewSubscriptionHandler(logger *slog.Logger, billing billingv1connect.BillingServiceClient, cat plancatalogue.Catalogue) *SubscriptionHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubscriptionHandler{logger: logger, billing: billing, catalogue: cat}
}

// entitlementView is the string-decimal-safe JSON projection of an Entitlement.
type entitlementView struct {
	RPM                      int      `json:"rpm"`
	TPM                      int      `json:"tpm"`
	QPS                      int      `json:"qps"`
	MonthlyIncludedCreditUSD string   `json:"monthly_included_credit_usd"`
	MonthlyQuotaUSD          string   `json:"monthly_quota_usd"`
	Features                 []string `json:"features"`
}

func viewOf(e plancatalogue.Entitlement) entitlementView {
	f := e.Features
	if f == nil {
		f = []string{}
	}
	return entitlementView{
		RPM:                      e.RPM,
		TPM:                      e.TPM,
		QPS:                      e.QPS,
		MonthlyIncludedCreditUSD: e.MonthlyIncludedCreditUSD,
		MonthlyQuotaUSD:          e.MonthlyQuotaUSD,
		Features:                 f,
	}
}

type subscriptionView struct {
	Plan             string          `json:"plan"`
	Status           string          `json:"status"`
	CurrentPeriodEnd string          `json:"current_period_end,omitempty"`
	Entitlements     entitlementView `json:"entitlements"`
}

// GetSubscription handles GET /v1/billing/subscription — the caller's current
// plan + resolved entitlements + period end (default-free when no active sub).
func (h *SubscriptionHandler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	resp, err := h.billing.GetSubscription(ctx, connect.NewRequest(&billingv1.GetSubscriptionRequest{UserId: userID}))
	if err != nil {
		h.writeSubErr(w, ctx, "get_subscription", err)
		return
	}
	plan := plancatalogue.PlanKey(resp.Msg.GetPlan())
	ent, eerr := h.catalogue.Entitlements(plan)
	if eerr != nil {
		// Defensive: an unknown plan from billing → resolve free (fail-safe-low).
		plan = plancatalogue.PlanFree
		ent, _ = h.catalogue.Entitlements(plancatalogue.PlanFree)
	}
	writeJSON(w, http.StatusOK, subscriptionView{
		Plan:             string(plan),
		Status:           resp.Msg.GetStatus(),
		CurrentPeriodEnd: resp.Msg.GetCurrentPeriodEnd(),
		Entitlements:     viewOf(ent),
	})
}

type changePlanRequest struct {
	Plan string `json:"plan"`
}

type changePlanView struct {
	Plan                string `json:"plan"`
	Direction           string `json:"direction"`
	DeferredToPeriodEnd bool   `json:"deferred_to_period_end"`
}

// ChangePlan handles PUT /v1/billing/subscription — upgrade/downgrade.
func (h *SubscriptionHandler) ChangePlan(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, false)
}

// Cancel handles DELETE /v1/billing/subscription — cancel → free at period end.
func (h *SubscriptionHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, true)
}

func (h *SubscriptionHandler) change(w http.ResponseWriter, r *http.Request, cancel bool) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}
	if h.billing == nil {
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_service_unavailable", "Billing service not configured.", nil)
		return
	}
	var plan string
	if !cancel {
		var req changePlanRequest
		if err := decodeJSONBody(r, &req); err != nil {
			_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Malformed request body.", nil)
			return
		}
		plan = strings.ToLower(strings.TrimSpace(req.Plan))
		// Validate against the in-process catalogue up-front (fast reject — Medium-1).
		if !h.catalogue.Has(plancatalogue.PlanKey(plan)) {
			p := req.Plan
			_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Unknown subscription plan. Supported: free, pro, team, enterprise.", &p)
			return
		}
	}
	resp, err := h.billing.ChangePlan(ctx, connect.NewRequest(&billingv1.ChangePlanRequest{
		UserId:  userID,
		NewPlan: plan,
		Cancel:  cancel,
	}))
	if err != nil {
		h.writeSubErr(w, ctx, "change_plan", err)
		return
	}
	writeJSON(w, http.StatusOK, changePlanView{
		Plan:                resp.Msg.GetNewPlan(),
		Direction:           resp.Msg.GetDirection(),
		DeferredToPeriodEnd: resp.Msg.GetDeferredToPeriodEnd(),
	})
}

type planView struct {
	Key          string          `json:"key"`
	DisplayName  string          `json:"display_name"`
	PriceUSD     string          `json:"price_usd"`
	Entitlements entitlementView `json:"entitlements"`
}

// GetPlans handles GET /v1/billing/plans — the public tier catalogue for the
// pricing page. Renders the in-process catalogue (BR-E-5 — same SoT the gateway
// enforces); no RPC, no auth dependency on billing-svc.
func (h *SubscriptionHandler) GetPlans(w http.ResponseWriter, r *http.Request) {
	plans := h.catalogue.List()
	out := make([]planView, 0, len(plans))
	for _, p := range plans {
		out = append(out, planView{
			Key:          string(p.Key),
			DisplayName:  p.DisplayName,
			PriceUSD:     p.PriceUSD,
			Entitlements: viewOf(p.Entitlement),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": out})
}

// writeSubErr maps a billing-svc Connect error to the canonical envelope:
// InvalidArgument → 400_invalid_payment_request (unknown plan, Medium-1);
// Unavailable → 402_payment_failed (provider-domain failure, REUSE 7.3);
// anything else → 500.
func (h *SubscriptionHandler) writeSubErr(w http.ResponseWriter, ctx context.Context, label string, err error) {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_payment_request", "Unknown subscription plan. Supported: free, pro, team, enterprise.", nil)
	case connect.CodeUnavailable:
		_ = openaierr.Write(w, ctx, http.StatusPaymentRequired, "402_payment_failed", "The payment provider could not process the plan change.", nil)
	default:
		h.logger.ErrorContext(ctx, "billing_subscription_upstream_failed",
			slog.String("event", "billing_subscription_upstream_failed"),
			slog.String("op", label),
			slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "Unable to process the subscription request.", nil)
	}
}
