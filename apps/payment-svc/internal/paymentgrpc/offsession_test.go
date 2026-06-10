// Story 7.7 — 7.7-UNIT-031: a provider that does not implement OffSessionProvider
// (a non-card channel) yields ErrOffSessionUnsupported at the handler → no charge.
package paymentgrpc

import (
	"context"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// nonCardProvider implements ONLY the base PaymentProvider seam (like USDC/Alipay+/
// WeChat) — it is NOT an OffSessionProvider.
type nonCardProvider struct{}

func (nonCardProvider) Name() string { return "coinbase" }
func (nonCardProvider) CreateCheckout(context.Context, provider.Order) (provider.CheckoutResult, error) {
	return provider.CheckoutResult{}, nil
}
func (nonCardProvider) CreateSubscription(context.Context, provider.Subscription) (provider.SubscriptionResult, error) {
	return provider.SubscriptionResult{}, nil
}
func (nonCardProvider) VerifyWebhook(context.Context, []byte, http.Header) (provider.VerifiedEvent, error) {
	return provider.VerifiedEvent{}, nil
}

func TestChargeOffSession_NonCardUnsupported(t *testing.T) {
	reg := provider.NewRegistry(nonCardProvider{})
	s := NewServer(reg, nil)
	_, err := s.ChargeOffSession(context.Background(), connect.NewRequest(&paymentv1.ChargeOffSessionRequest{
		UserId: "u1", PmToken: "tok", Amount: "20.00", OrderId: "o1", PaymentProvider: "coinbase",
	}))
	if err == nil {
		t.Fatal("expected ErrOffSessionUnsupported for a non-card provider")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}
