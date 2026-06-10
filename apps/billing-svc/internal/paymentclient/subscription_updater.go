// subscription_updater.go — Story 7.8: adapts the payment-svc PaymentService
// Connect client to the billing-svc subscription.ProviderUpdater seam. It maps
// the (prorate, atPeriodEnd) booleans the orchestrator resolves into the Stripe/
// PayPal directives (proration_behavior + schedule). NO He-API credit (BR-S-4/5).
package paymentclient

import (
	"context"

	connect "connectrpc.com/connect"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

// SubscriptionUpdater implements subscription.ProviderUpdater over the payment
// Connect client.
type SubscriptionUpdater struct {
	client paymentv1connect.PaymentServiceClient
}

// NewSubscriptionUpdater builds the adapter.
func NewSubscriptionUpdater(client paymentv1connect.PaymentServiceClient) *SubscriptionUpdater {
	return &SubscriptionUpdater{client: client}
}

// UpdateProviderSubscription calls payment-svc. prorate → Stripe
// proration_behavior (create_prorations | none); atPeriodEnd → schedule
// (period_end | now). A transport/provider error is returned to the orchestrator
// (which surfaces it as a 402 at the gateway — no plan transition).
func (u *SubscriptionUpdater) UpdateProviderSubscription(ctx context.Context, provider, extSubID, newPlan string, prorate, atPeriodEnd bool) error {
	prorationBehavior := "none"
	if prorate {
		prorationBehavior = "create_prorations"
	}
	schedule := "now"
	if atPeriodEnd {
		schedule = "period_end"
	}
	if provider == "" {
		provider = "stripe"
	}
	_, err := u.client.UpdateProviderSubscription(ctx, connect.NewRequest(&paymentv1.UpdateProviderSubscriptionRequest{
		ExternalSubscriptionId: extSubID,
		NewPlan:                newPlan,
		PaymentProvider:        provider,
		ProrationBehavior:      prorationBehavior,
		Schedule:               schedule,
	}))
	return err
}
