// Package paymentgrpc implements the he.payment.v1.PaymentService Connect-RPC
// handler (Story 7.3). CreateCheckout / CreateSubscription resolve the requested
// provider from the seam Registry and delegate — the handler is provider-agnostic
// (Q-PROVIDER-SEAM), so 7.4-7.6 add channels without touching it.
package paymentgrpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// Server implements paymentv1connect.PaymentServiceHandler.
type Server struct {
	providers *provider.Registry
	logger    *slog.Logger
}

// NewServer constructs the PaymentService handler over the provider Registry.
func NewServer(providers *provider.Registry, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{providers: providers, logger: logger}
}

var errMissingArgs = errors.New("missing required field")

// CreateCheckout opens a provider-hosted checkout for a pending recharge order.
func (s *Server) CreateCheckout(ctx context.Context, req *connect.Request[paymentv1.CreateCheckoutRequest]) (*connect.Response[paymentv1.CreateCheckoutResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.GetOrderId()) == "" || strings.TrimSpace(m.GetUserId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMissingArgs)
	}
	prov, err := s.providers.Get(m.GetPaymentProvider())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res, err := prov.CreateCheckout(ctx, provider.Order{
		OrderID:  m.GetOrderId(),
		UserID:   m.GetUserId(),
		Amount:   m.GetAmount(),
		Currency: m.GetCurrency(),
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "payment_create_checkout_failed",
			slog.String("event", "payment_create_checkout_failed"),
			slog.String("provider", m.GetPaymentProvider()),
			slog.String("order_id", m.GetOrderId()),
			slog.String("error", err.Error()),
		)
		// A provider-side failure is a payment-domain error (the gateway maps it to
		// 402_payment_failed), NOT our-infra 5xx.
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&paymentv1.CreateCheckoutResponse{
		CheckoutUrl:     res.CheckoutURL,
		ClientToken:     res.ClientToken,
		ExternalOrderId: res.ExternalOrderID,
	}), nil
}

// CreateSubscription creates a provider subscription (RAIL only — Q-SUBSCOPE).
func (s *Server) CreateSubscription(ctx context.Context, req *connect.Request[paymentv1.CreateSubscriptionRequest]) (*connect.Response[paymentv1.CreateSubscriptionResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.GetSubscriptionId()) == "" || strings.TrimSpace(m.GetUserId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMissingArgs)
	}
	prov, err := s.providers.Get(m.GetPaymentProvider())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res, err := prov.CreateSubscription(ctx, provider.Subscription{
		SubscriptionID: m.GetSubscriptionId(),
		UserID:         m.GetUserId(),
		Plan:           m.GetPlan(),
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "payment_create_subscription_failed",
			slog.String("event", "payment_create_subscription_failed"),
			slog.String("provider", m.GetPaymentProvider()),
			slog.String("subscription_id", m.GetSubscriptionId()),
			slog.String("error", err.Error()),
		)
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&paymentv1.CreateSubscriptionResponse{
		CheckoutUrl:            res.CheckoutURL,
		ExternalSubscriptionId: res.ExternalSubscriptionID,
	}), nil
}
