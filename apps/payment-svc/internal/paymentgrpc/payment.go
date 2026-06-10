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

// offSession resolves a provider and asserts the Story-7.7 off-session capability.
// A non-card provider → ErrOffSessionUnsupported (Q-OFFSESSION). Defaults to stripe.
func (s *Server) offSession(providerName string) (provider.OffSessionProvider, error) {
	if strings.TrimSpace(providerName) == "" {
		providerName = "stripe"
	}
	prov, err := s.providers.Get(providerName)
	if err != nil {
		return nil, err
	}
	osp, ok := prov.(provider.OffSessionProvider)
	if !ok {
		return nil, provider.ErrOffSessionUnsupported
	}
	return osp, nil
}

// ChargeOffSession charges a stored token with no user present (Story 7.7 AC1).
// The token is SECRET-grade — it is NEVER logged. The credit settles via the 7.3
// webhook (the order_id metadata binds it); this RPC only starts the charge.
func (s *Server) ChargeOffSession(ctx context.Context, req *connect.Request[paymentv1.ChargeOffSessionRequest]) (*connect.Response[paymentv1.ChargeOffSessionResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.GetUserId()) == "" || strings.TrimSpace(m.GetPmToken()) == "" || strings.TrimSpace(m.GetOrderId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMissingArgs)
	}
	osp, err := s.offSession(m.GetPaymentProvider())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ext, status, cerr := osp.ChargeOffSession(ctx, m.GetOrderId(), m.GetPmToken(), m.GetAmount())
	if cerr != nil {
		// Log order_id + provider only — NEVER the token or the raw provider error
		// (which must not embed the secret).
		s.logger.ErrorContext(ctx, "payment_charge_off_session_failed",
			slog.String("event", "payment_charge_off_session_failed"),
			slog.String("provider", m.GetPaymentProvider()),
			slog.String("order_id", m.GetOrderId()),
		)
		return nil, connect.NewError(connect.CodeUnavailable, errChargeFailed)
	}
	return connect.NewResponse(&paymentv1.ChargeOffSessionResponse{ExternalOrderId: ext, Status: status}), nil
}

// CreateSetupIntent begins a Stripe SetupIntent to save an off-session token.
func (s *Server) CreateSetupIntent(ctx context.Context, req *connect.Request[paymentv1.CreateSetupIntentRequest]) (*connect.Response[paymentv1.CreateSetupIntentResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.GetUserId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMissingArgs)
	}
	osp, err := s.offSession("stripe")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	clientSecret, id, serr := osp.CreateSetupIntent(ctx, m.GetUserId())
	if serr != nil {
		s.logger.ErrorContext(ctx, "payment_create_setup_intent_failed",
			slog.String("event", "payment_create_setup_intent_failed"))
		return nil, connect.NewError(connect.CodeUnavailable, errChargeFailed)
	}
	return connect.NewResponse(&paymentv1.CreateSetupIntentResponse{ClientSecret: clientSecret, SetupIntentId: id}), nil
}

// RetrievePaymentMethod fetches the confirmed token off a SetupIntent (server-side
// — the token is never client-asserted, defeating cross-user injection). The
// returned token is display-redacted in logs (it is part of the response, not logged).
func (s *Server) RetrievePaymentMethod(ctx context.Context, req *connect.Request[paymentv1.RetrievePaymentMethodRequest]) (*connect.Response[paymentv1.RetrievePaymentMethodResponse], error) {
	m := req.Msg
	if strings.TrimSpace(m.GetSetupIntentId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMissingArgs)
	}
	osp, err := s.offSession("stripe")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	token, brand, last4, rerr := osp.RetrievePaymentMethod(ctx, m.GetSetupIntentId())
	if rerr != nil {
		s.logger.ErrorContext(ctx, "payment_retrieve_payment_method_failed",
			slog.String("event", "payment_retrieve_payment_method_failed"))
		return nil, connect.NewError(connect.CodeUnavailable, errChargeFailed)
	}
	return connect.NewResponse(&paymentv1.RetrievePaymentMethodResponse{ProviderPmToken: token, Brand: brand, Last4: last4}), nil
}

var errChargeFailed = errors.New("off-session payment operation failed")
