// Story 7.3 — 7.3-CONTRACT-002 / 7.3-CONTRACT-003. payment.pb.go in this package
// is hand-authored (buf/protoc cannot run locally — [[project_toolchain_env_-
// limits]]). This test is the correctness gate: it proves the hand-built
// FileDescriptorProto loads without panic at init() AND that PaymentEvent +
// CreateCheckout{Request,Response} + CreateSubscription{Request,Response} round-
// trip through proto binary marshal/unmarshal byte-for-byte. A malformed rawDesc
// / mismatched goTypes/depIdxs would panic in init or fail the reflective marshal.
package paymentv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestPaymentEventRoundTrip(t *testing.T) {
	in := &PaymentEvent{
		OrderId:                "55555555-5555-5555-5555-555555555555",
		UserId:                 "11111111-1111-1111-1111-111111111111",
		PaymentProvider:        "stripe",
		ExternalOrderId:        "cs_test_abc123",
		SettledAmount:          "50.00", // string-decimal money discipline (Q-AMOUNT / Q-Spec-4)
		Currency:               "USD",
		Status:                 "paid",
		EventType:              "recharge",
		ExternalSubscriptionId: "",
		Ts:                     "2026-06-09T12:34:56Z",
	}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out PaymentEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, &out)
	}
	// Spot-check the credit-bearing fields so a silent tag-shift is caught.
	if out.GetSettledAmount() != "50.00" || out.GetPaymentProvider() != "stripe" ||
		out.GetStatus() != "paid" || out.GetEventType() != "recharge" {
		t.Fatalf("field decode mismatch: %+v", &out)
	}
}

func TestCreateCheckoutMessagesRoundTrip(t *testing.T) {
	req := &CreateCheckoutRequest{
		OrderId:         "55555555-5555-5555-5555-555555555555",
		UserId:          "11111111-1111-1111-1111-111111111111",
		Amount:          "50.00",
		Currency:        "USD",
		PaymentProvider: "stripe",
	}
	rb, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var reqOut CreateCheckoutRequest
	if err := proto.Unmarshal(rb, &reqOut); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if !proto.Equal(req, &reqOut) {
		t.Fatalf("req mismatch:\n in=%+v\nout=%+v", req, &reqOut)
	}

	resp := &CreateCheckoutResponse{
		CheckoutUrl:     "https://checkout.stripe.com/c/pay/cs_test_abc123",
		ClientToken:     "",
		ExternalOrderId: "cs_test_abc123",
	}
	pb, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var respOut CreateCheckoutResponse
	if err := proto.Unmarshal(pb, &respOut); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if respOut.GetCheckoutUrl() != resp.GetCheckoutUrl() || respOut.GetExternalOrderId() != "cs_test_abc123" {
		t.Fatalf("resp mismatch: %+v", &respOut)
	}
}

func TestCreateSubscriptionMessagesRoundTrip(t *testing.T) {
	req := &CreateSubscriptionRequest{
		SubscriptionId:  "66666666-6666-6666-6666-666666666666",
		UserId:          "11111111-1111-1111-1111-111111111111",
		Plan:            "pro",
		PaymentProvider: "stripe",
	}
	rb, _ := proto.Marshal(req)
	var reqOut CreateSubscriptionRequest
	if err := proto.Unmarshal(rb, &reqOut); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if reqOut.GetPlan() != "pro" || reqOut.GetSubscriptionId() != req.GetSubscriptionId() {
		t.Fatalf("req mismatch: %+v", &reqOut)
	}

	resp := &CreateSubscriptionResponse{
		CheckoutUrl:            "https://checkout.stripe.com/c/pay/sub_xxx",
		ExternalSubscriptionId: "sub_xxx",
	}
	pb, _ := proto.Marshal(resp)
	var respOut CreateSubscriptionResponse
	if err := proto.Unmarshal(pb, &respOut); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if respOut.GetExternalSubscriptionId() != "sub_xxx" {
		t.Fatalf("resp mismatch: %+v", &respOut)
	}
}
