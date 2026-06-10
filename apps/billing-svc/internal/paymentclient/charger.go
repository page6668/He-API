// Package paymentclient adapts the payment-svc PaymentService Connect client to
// the billing-svc autorecharge.Charger seam (Story 7.7). It maps an off-session
// top-up to a he.payment.v1.ChargeOffSession call carrying OUR order id (so the
// 7.3 webhook resolves the credit) and a Stripe-card provider (Q-OFFSESSION). The
// pm token flows straight through to the request and is NEVER logged here.
package paymentclient

import (
	"context"

	connect "connectrpc.com/connect"

	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1/paymentv1connect"
)

// Charger implements autorecharge.Charger over the payment-svc Connect client.
type Charger struct {
	client paymentv1connect.PaymentServiceClient
}

// NewCharger builds the adapter.
func NewCharger(client paymentv1connect.PaymentServiceClient) *Charger {
	return &Charger{client: client}
}

// ChargeOffSession calls payment-svc. A transport error is returned to the trigger
// (which marks the order failed + counts it toward auto-disable); a clean decline
// arrives as status="failed" in the response.
func (c *Charger) ChargeOffSession(ctx context.Context, userID, orderID, pmToken, amount, currency string) (string, string, error) {
	if currency == "" {
		currency = "USD"
	}
	resp, err := c.client.ChargeOffSession(ctx, connect.NewRequest(&paymentv1.ChargeOffSessionRequest{
		UserId:          userID,
		OrderId:         orderID,
		PmToken:         pmToken,
		Amount:          amount,
		Currency:        currency,
		PaymentProvider: "stripe",
	}))
	if err != nil {
		return "", "", err
	}
	return resp.Msg.GetExternalOrderId(), resp.Msg.GetStatus(), nil
}
