// Story 7.8 — BillingService subscription-tier RPCs (GetSubscription / ChangePlan
// / GetEntitlements). billing-svc is the SoT for the he_api.subscriptions rail +
// the SOLE writer of the gateway entitlement snapshot (BR-E-3); the tier
// SEMANTICS live in packages/plan-catalogue (Q-PLAN-CATALOG). These handlers
// resolve the AUTHENTICATED user (user_id is server-supplied by the gateway from
// the bearer/JWT identity — BR-S-7, cross-user mutation impossible).
package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/billing-svc/internal/subscription"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// resolvePlan returns the user's effective plan key (active subscription, else
// free — Q-PLAN-DEFAULT / BR-S-8) plus the period end (empty when free/unknown).
func (s *Server) resolvePlan(ctx context.Context, userID string) (plancatalogue.PlanKey, string, error) {
	if s.subReader == nil {
		return plancatalogue.PlanFree, "", nil
	}
	sub, found, err := s.subReader.CurrentSubscription(ctx, userID)
	if err != nil {
		return plancatalogue.PlanFree, "", err
	}
	if !found || sub.Status != "active" || !s.catalogue.Has(sub.Plan) {
		return plancatalogue.PlanFree, "", nil // default-free / fail-safe-low
	}
	return sub.Plan, sub.CurrentPeriodEnd, nil
}

// GetSubscription resolves the caller's current plan + status + period end. A
// user with no active subscription resolves to free (Q-PLAN-DEFAULT).
func (s *Server) GetSubscription(ctx context.Context, req *connect.Request[billingv1.GetSubscriptionRequest]) (*connect.Response[billingv1.GetSubscriptionResponse], error) {
	if !s.subsWired {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoSubscriptions)
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidUserID)
	}
	plan, periodEnd, err := s.resolvePlan(ctx, userID)
	if err != nil {
		s.logger.ErrorContext(ctx, "billing_get_subscription_failed",
			slog.String("error", err.Error()))
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	status := "active"
	if plan == plancatalogue.PlanFree {
		status = "active" // free is always "active" (the implicit default tier)
	}
	return connect.NewResponse(&billingv1.GetSubscriptionResponse{
		Plan:             string(plan),
		Status:           status,
		CurrentPeriodEnd: periodEnd,
	}), nil
}

// ChangePlan upgrades/downgrades (or cancels) the caller's subscription. An
// unknown plan → InvalidArgument (the gateway maps it to the REUSED 7.3
// 400_invalid_payment_request — Medium-1). A provider-domain failure →
// CodeUnavailable (gateway → 402_payment_failed).
func (s *Server) ChangePlan(ctx context.Context, req *connect.Request[billingv1.ChangePlanRequest]) (*connect.Response[billingv1.ChangePlanResponse], error) {
	if !s.subsWired || s.subs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoSubscriptions)
	}
	m := req.Msg
	userID := strings.TrimSpace(m.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidUserID)
	}
	// DELETE (cancel) reverts to free at period end (Q-REFUND deferral: no
	// mid-period refund). Modelled as a downgrade to free.
	newPlan := plancatalogue.PlanKey(strings.TrimSpace(m.GetNewPlan()))
	if m.GetCancel() {
		newPlan = plancatalogue.PlanFree
	}
	res, err := s.subs.ChangePlan(ctx, userID, newPlan)
	if err != nil {
		if errors.Is(err, subscription.ErrUnknownPlan) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.logger.ErrorContext(ctx, "billing_change_plan_failed",
			slog.String("error", err.Error()))
		// A provider update failure is a payment-domain error (→ 402), not 5xx.
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&billingv1.ChangePlanResponse{
		Direction:           res.Direction.String(),
		NewPlan:             string(res.NewPlan),
		DeferredToPeriodEnd: res.DeferredToPeriodEnd,
	}), nil
}

// GetEntitlements resolves a caller's tier entitlement from the catalogue (money
// is string-decimal — Q-Spec-4).
func (s *Server) GetEntitlements(ctx context.Context, req *connect.Request[billingv1.GetEntitlementsRequest]) (*connect.Response[billingv1.GetEntitlementsResponse], error) {
	if !s.subsWired {
		return nil, connect.NewError(connect.CodeUnimplemented, errNoSubscriptions)
	}
	userID := strings.TrimSpace(req.Msg.GetUserId())
	if userID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidUserID)
	}
	plan, _, err := s.resolvePlan(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	ent, err := s.catalogue.Entitlements(plan)
	if err != nil {
		// Catalogue drift is impossible (resolvePlan returns only known/free), but
		// fail safe-low rather than error.
		ent, _ = s.catalogue.Entitlements(plancatalogue.PlanFree)
	}
	return connect.NewResponse(&billingv1.GetEntitlementsResponse{
		Plan:                     string(ent.Plan),
		Rpm:                      int64(ent.RPM),
		Tpm:                      int64(ent.TPM),
		Qps:                      int64(ent.QPS),
		MonthlyIncludedCreditUsd: ent.MonthlyIncludedCreditUSD,
		MonthlyQuotaUsd:          ent.MonthlyQuotaUSD,
	}), nil
}

const errNoSubscriptions = constErr("subscription surface not configured")
