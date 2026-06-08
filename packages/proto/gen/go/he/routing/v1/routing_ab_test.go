package routingv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// 6.4-CONTRACT-001 — the THREE 6.1-preshaped A/B fields wire round-trip with NO
// new field numbers (Story 6.4 first-realises them): request ab_models=4,
// response is_ab_test=3 + ab_selected_models=4. Proves the gateway-populated
// request field and the routing-svc-populated response fields encode/decode
// identically — no buf breaking change (the field numbers existed since 6.1).
func TestAB_PreshapedFields_WireRoundTrip(t *testing.T) {
	t.Parallel()

	// Request: gateway populates ab_models (field 4) from X-He-AB-Models.
	reqOrig := &SelectModelRequest{
		UserId:      "u1",
		AbModels:    []string{"qwen-max", "deepseek-v3"},
		HeRequestId: "req_abc",
	}
	reqWire, err := proto.Marshal(reqOrig)
	if err != nil {
		t.Fatalf("Marshal request: %v", err)
	}
	var reqGot SelectModelRequest
	if err := proto.Unmarshal(reqWire, &reqGot); err != nil {
		t.Fatalf("Unmarshal request: %v", err)
	}
	if !proto.Equal(reqOrig, &reqGot) {
		t.Fatalf("request round-trip mismatch:\n orig = %+v\n  got = %+v", reqOrig, &reqGot)
	}
	if ab := reqGot.GetAbModels(); len(ab) != 2 || ab[0] != "qwen-max" || ab[1] != "deepseek-v3" {
		t.Errorf("GetAbModels() = %v, want [qwen-max deepseek-v3]", ab)
	}

	// Response: routing-svc sets is_ab_test (field 3) + ab_selected_models (field 4).
	respOrig := &SelectModelResponse{
		IsAbTest:         true,
		AbSelectedModels: []string{"qwen-max", "deepseek-v3"},
	}
	respWire, err := proto.Marshal(respOrig)
	if err != nil {
		t.Fatalf("Marshal response: %v", err)
	}
	var respGot SelectModelResponse
	if err := proto.Unmarshal(respWire, &respGot); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if !proto.Equal(respOrig, &respGot) {
		t.Fatalf("response round-trip mismatch:\n orig = %+v\n  got = %+v", respOrig, &respGot)
	}
	if !respGot.GetIsAbTest() {
		t.Error("GetIsAbTest() = false, want true")
	}
	if legs := respGot.GetAbSelectedModels(); len(legs) != 2 || legs[0] != "qwen-max" || legs[1] != "deepseek-v3" {
		t.Errorf("GetAbSelectedModels() = %v, want [qwen-max deepseek-v3]", legs)
	}
}
