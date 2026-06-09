// Story 7.3 webhook ingress status-code discipline (7.3-INT-018/019, BR-W-5):
//   - forged signature → 400, body never parsed, NOTHING emitted;
//   - genuine event → 200 + exactly one payment.completed emitted;
//   - verified no-op (refund) → 200, nothing emitted (Q-REFUND);
//   - verified but produce FAILS → 502 so the provider redelivers.
package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/payment-svc/internal/provider"
	paymentv1 "github.com/he-api/he-api/packages/proto/gen/go/he/payment/v1"
)

// fakeProvider is a stub PaymentProvider whose VerifyWebhook is scripted.
type fakeProvider struct {
	name string
	ev   provider.VerifiedEvent
	err  error
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) CreateCheckout(context.Context, provider.Order) (provider.CheckoutResult, error) {
	return provider.CheckoutResult{}, nil
}
func (f *fakeProvider) CreateSubscription(context.Context, provider.Subscription) (provider.SubscriptionResult, error) {
	return provider.SubscriptionResult{}, nil
}
func (f *fakeProvider) VerifyWebhook(context.Context, []byte, http.Header) (provider.VerifiedEvent, error) {
	return f.ev, f.err
}

// fakeEmitter records emitted events; emitErr forces an emit failure.
type fakeEmitter struct {
	events   []*paymentv1.PaymentEvent
	emitErr  error
	callerCt int
}

func (e *fakeEmitter) Emit(_ context.Context, ev *paymentv1.PaymentEvent) error {
	e.callerCt++
	if e.emitErr != nil {
		return e.emitErr
	}
	e.events = append(e.events, ev)
	return nil
}

func do(h *Handler, providerName, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/"+providerName, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Handle(providerName)(rec, req)
	return rec
}

func TestHandle_ForgedSignature_400_NoEmit(t *testing.T) {
	fp := &fakeProvider{name: "stripe", err: provider.ErrSignatureInvalid}
	em := &fakeEmitter{}
	h := New(provider.NewRegistry(fp), em, nil)

	rec := do(h, "stripe", `{"anything":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forged: status = %d, want 400", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("forged: emitter called %d times, want 0 (body never trusted)", em.callerCt)
	}
}

func TestHandle_GenuineRecharge_200_EmitsOnce(t *testing.T) {
	fp := &fakeProvider{name: "stripe", ev: provider.VerifiedEvent{
		Provider: "stripe", Kind: provider.EventRechargePaid, OrderID: "order-1",
		ExternalOrderID: "pi_1", SettledAmount: "50.00", Currency: "USD", Status: "paid",
	}}
	em := &fakeEmitter{}
	h := New(provider.NewRegistry(fp), em, nil)

	rec := do(h, "stripe", `{"genuine":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("genuine: status = %d, want 200", rec.Code)
	}
	if len(em.events) != 1 {
		t.Fatalf("genuine: emitted %d events, want 1", len(em.events))
	}
	got := em.events[0]
	if got.GetOrderId() != "order-1" || got.GetSettledAmount() != "50.00" || got.GetEventType() != "recharge_paid" {
		t.Errorf("emitted event mismatch: %+v", got)
	}
	// PCI / BR-R-4: the event carries no user_id from the client; billing resolves
	// the user from the order. settled_amount is the provider truth.
	if got.GetUserId() != "" {
		t.Errorf("event should not carry a client user_id, got %q", got.GetUserId())
	}
}

func TestHandle_UnhandledEvent_200_NoEmit(t *testing.T) {
	fp := &fakeProvider{name: "stripe", ev: provider.VerifiedEvent{Provider: "stripe", Kind: provider.EventUnhandled}}
	em := &fakeEmitter{}
	h := New(provider.NewRegistry(fp), em, nil)

	rec := do(h, "stripe", `{"type":"charge.refunded"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unhandled: status = %d, want 200 (ACK no-op)", rec.Code)
	}
	if em.callerCt != 0 {
		t.Fatalf("unhandled: emitter called %d times, want 0 (Q-REFUND)", em.callerCt)
	}
}

func TestHandle_EmitFails_502_ForRedelivery(t *testing.T) {
	fp := &fakeProvider{name: "stripe", ev: provider.VerifiedEvent{
		Provider: "stripe", Kind: provider.EventRechargePaid, OrderID: "order-1", SettledAmount: "50.00", Status: "paid",
	}}
	em := &fakeEmitter{emitErr: errors.New("kafka down")}
	h := New(provider.NewRegistry(fp), em, nil)

	rec := do(h, "stripe", `{"genuine":true}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("emit-fail: status = %d, want 502 (provider redelivers, BR-W-5)", rec.Code)
	}
}

func TestHandle_UnknownProvider_404(t *testing.T) {
	h := New(provider.NewRegistry(), nil, nil)
	rec := do(h, "dogecoin", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: status = %d, want 404", rec.Code)
	}
}
