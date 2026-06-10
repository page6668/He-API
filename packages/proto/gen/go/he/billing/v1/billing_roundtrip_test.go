// Story 7.1 — 7.1-CONTRACT-002 / 7.1-CONTRACT-003. The billing.pb.go in this
// package is hand-authored (buf/protoc cannot run locally — [[project_toolchain_-
// env_limits]]). This test is the correctness gate: it proves the hand-built
// FileDescriptorProto loads without panic at init() AND that UsageEvent +
// CheckBalance{Request,Response} round-trip through proto binary marshal/unmarshal
// byte-for-byte. A malformed rawDesc / mismatched goTypes would panic in init or
// fail the reflective marshal here.
package billingv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// Story 7.7 — 7.7-CONTRACT-001: the BillingService additive messages round-trip
// (including the bytes pdf field + bool fields) and the descriptor loads at init.
func TestStory77BillingMessagesRoundTrip(t *testing.T) {
	ar := &SetAutoRechargeRequest{
		UserId:          "11111111-1111-1111-1111-111111111111",
		Enabled:         true,
		ThresholdUsd:    "5.00",
		AmountUsd:       "20.00",
		PaymentMethodId: "22222222-2222-2222-2222-222222222222",
	}
	ab, _ := proto.Marshal(ar)
	var arOut SetAutoRechargeRequest
	if err := proto.Unmarshal(ab, &arOut); err != nil {
		t.Fatalf("unmarshal auto-recharge: %v", err)
	}
	if !proto.Equal(ar, &arOut) {
		t.Fatalf("auto-recharge mismatch:\n in=%+v\nout=%+v", ar, &arOut)
	}
	if !arOut.GetEnabled() || arOut.GetThresholdUsd() != "5.00" || arOut.GetAmountUsd() != "20.00" {
		t.Fatalf("auto-recharge field decode mismatch: %+v", &arOut)
	}

	pdf := &GetInvoicePdfResponse{Pdf: []byte("%PDF-1.4 fake"), Filename: "invoice-2026-06.pdf", Found: true}
	pb, _ := proto.Marshal(pdf)
	var pdfOut GetInvoicePdfResponse
	if err := proto.Unmarshal(pb, &pdfOut); err != nil {
		t.Fatalf("unmarshal pdf: %v", err)
	}
	if string(pdfOut.GetPdf()) != "%PDF-1.4 fake" || !pdfOut.GetFound() || pdfOut.GetFilename() != "invoice-2026-06.pdf" {
		t.Fatalf("pdf field decode mismatch: %+v", &pdfOut)
	}

	del := &DeletePaymentMethodResponse{Deleted: true, AutoRechargeDisabled: true}
	db, _ := proto.Marshal(del)
	var delOut DeletePaymentMethodResponse
	if err := proto.Unmarshal(db, &delOut); err != nil {
		t.Fatalf("unmarshal delete: %v", err)
	}
	if !delOut.GetDeleted() || !delOut.GetAutoRechargeDisabled() {
		t.Fatalf("delete mismatch: %+v", &delOut)
	}
}

func TestUsageEventRoundTrip(t *testing.T) {
	in := &UsageEvent{
		LedgerKey:        "req_abc123:0",
		HeRequestId:      "req_abc123",
		UserId:           "11111111-1111-1111-1111-111111111111",
		ApiKeyId:         "22222222-2222-2222-2222-222222222222",
		TeamId:           "",
		Model:            "qwen-max",
		PromptTokens:     1500,
		CompletionTokens: 800,
		TotalTokens:      2300,
		IsStreaming:      true,
		IsAbLeg:          true,
		Ts:               "2026-06-09T12:34:56Z",
		BillingMode:      BillingMode_BILLING_MODE_PER_TOKEN,
	}

	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out UsageEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !proto.Equal(in, &out) {
		t.Fatalf("round-trip mismatch:\n in = %+v\nout = %+v", in, &out)
	}

	// Spot-check a representative field of every wire type so a silent tag-shift
	// (e.g. a swapped field number) is caught, not just structural equality.
	if out.GetLedgerKey() != "req_abc123:0" || out.GetPromptTokens() != 1500 ||
		!out.GetIsStreaming() || out.GetBillingMode() != BillingMode_BILLING_MODE_PER_TOKEN {
		t.Fatalf("field decode mismatch: %+v", &out)
	}
}

func TestCheckBalanceMessagesRoundTrip(t *testing.T) {
	req := &CheckBalanceRequest{UserId: "33333333-3333-3333-3333-333333333333"}
	rb, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var reqOut CheckBalanceRequest
	if err := proto.Unmarshal(rb, &reqOut); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if reqOut.GetUserId() != req.GetUserId() {
		t.Fatalf("req mismatch: %q != %q", reqOut.GetUserId(), req.GetUserId())
	}

	// current_usd MUST be a string (Q-Spec-4 string-decimal money discipline).
	resp := &CheckBalanceResponse{CurrentUsd: "12.3400", Sufficient: true}
	pb, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var respOut CheckBalanceResponse
	if err := proto.Unmarshal(pb, &respOut); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if respOut.GetCurrentUsd() != "12.3400" || !respOut.GetSufficient() {
		t.Fatalf("resp mismatch: %+v", &respOut)
	}
}

// TestCreateRechargeOrderRoundTrip — 7.3-CONTRACT-001. The Story 7.3 additive
// CreateRechargeOrder{Request,Response} messages round-trip byte-for-byte AND
// the pre-existing UsageEvent/CheckBalance messages still decode (backward-compat
// guard for the additive descriptor edit). amount is a string-decimal (Q-Spec-4).
func TestCreateRechargeOrderRoundTrip(t *testing.T) {
	req := &CreateRechargeOrderRequest{
		UserId:          "44444444-4444-4444-4444-444444444444",
		Amount:          "50.00",
		Currency:        "USD",
		PaymentProvider: "stripe",
	}
	rb, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}
	var reqOut CreateRechargeOrderRequest
	if err := proto.Unmarshal(rb, &reqOut); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if !proto.Equal(req, &reqOut) {
		t.Fatalf("req round-trip mismatch:\n in=%+v\nout=%+v", req, &reqOut)
	}
	if reqOut.GetAmount() != "50.00" || reqOut.GetPaymentProvider() != "stripe" {
		t.Fatalf("req field decode mismatch: %+v", &reqOut)
	}

	resp := &CreateRechargeOrderResponse{OrderId: "55555555-5555-5555-5555-555555555555", Status: "pending"}
	pb, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var respOut CreateRechargeOrderResponse
	if err := proto.Unmarshal(pb, &respOut); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if respOut.GetOrderId() != resp.GetOrderId() || respOut.GetStatus() != "pending" {
		t.Fatalf("resp mismatch: %+v", &respOut)
	}

	// Backward-compat: the original messages still resolve under the new descriptor.
	ue := &UsageEvent{LedgerKey: "k", Model: "m", BillingMode: BillingMode_BILLING_MODE_PER_TOKEN}
	ub, _ := proto.Marshal(ue)
	var ueOut UsageEvent
	if err := proto.Unmarshal(ub, &ueOut); err != nil {
		t.Fatalf("usage_event regression: %v", err)
	}
	if ueOut.GetLedgerKey() != "k" {
		t.Fatalf("usage_event field regression: %+v", &ueOut)
	}
}

// TestBillingModeEnum asserts the enum descriptor resolved (a broken enum
// type_name in the descriptor would make this panic or return the wrong name).
func TestBillingModeEnum(t *testing.T) {
	if got := BillingMode_BILLING_MODE_PER_CALL.String(); got != "BILLING_MODE_PER_CALL" {
		t.Fatalf("enum String() = %q, want BILLING_MODE_PER_CALL", got)
	}
	if BillingMode_value["BILLING_MODE_PER_TOKEN"] != 1 {
		t.Fatalf("enum value map wrong")
	}
}
