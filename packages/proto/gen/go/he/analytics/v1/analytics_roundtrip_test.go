// Story 9.1 — 9.1-CONTRACT. The analytics.pb.go in this package is hand-authored
// (buf/protoc cannot run locally — [[project_toolchain_env_limits]]). This test
// is the correctness gate: it proves the hand-built FileDescriptorProto loads
// without panic at init() AND that UsageLogEvent round-trips through both proto
// binary AND protojson (the wire format for the `request.logged` Kafka topic).
// A malformed rawDesc / mismatched goTypes would panic in init or fail here.
package analyticsv1

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func sampleEvent() *UsageLogEvent {
	return &UsageLogEvent{
		HeRequestId:        "req_abcdef012345",
		UserId:             "11111111-1111-1111-1111-111111111111",
		ApiKeyId:           "22222222-2222-2222-2222-222222222222",
		Model:              "qwen-max",
		UpstreamModel:      "qwen-max-2025",
		RoutingStrategy:    "latency",
		SelectedByStrategy: "qwen-max-2025",
		StatusCode:         200,
		PromptTokens:       100,
		CompletionTokens:   200,
		TotalTokens:        300,
		CostUsd:            "0.001234",
		LatencyMsTotal:     420,
		LatencyMsGateway:   12,
		LatencyMsUpstream:  408,
		TtfbMs:             95,
		IsStreaming:        true,
		ClientIp:           "203.0.113.7",
		ClientCountry:      "SG",
		ErrorCode:          "",
		Ts:                 "2026-06-11T12:34:56Z",
	}
}

func TestUsageLogEventBinaryRoundTrip(t *testing.T) {
	in := sampleEvent()

	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out UsageLogEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round-trip mismatch:\n in = %+v\nout = %+v", in, &out)
	}

	// Spot-check a representative field of every wire type so a silent tag-shift
	// (swapped field number) is caught, not just structural equality.
	if out.GetHeRequestId() != "req_abcdef012345" || out.GetStatusCode() != 200 ||
		out.GetTotalTokens() != 300 || out.GetCostUsd() != "0.001234" ||
		!out.GetIsStreaming() || out.GetSelectedByStrategy() != "qwen-max-2025" {
		t.Fatalf("field decode mismatch: %+v", &out)
	}
}

// TestUsageLogEventProtojson proves the wire format used by the Kafka producer
// (default protojson — lowerCamelCase JSON names, the house precedent from
// billingemit) round-trips. cost_usd MUST stay a string-decimal, never a JSON
// number (Q-Spec-4 / Q-COST H-1).
func TestUsageLogEventProtojson(t *testing.T) {
	in := sampleEvent()

	body, err := protojson.Marshal(in)
	if err != nil {
		t.Fatalf("protojson marshal: %v", err)
	}
	js := string(body)
	// Default protojson emits the lowerCamelCase json_name (heRequestId, …),
	// matching billingemit. cost_usd stays a quoted string-decimal.
	for _, want := range []string{`"heRequestId"`, `"selectedByStrategy"`, `"statusCode"`, `"costUsd":"0.001234"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("protojson missing %s in: %s", want, js)
		}
	}

	var out UsageLogEvent
	if err := protojson.Unmarshal(body, &out); err != nil {
		t.Fatalf("protojson unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("protojson round-trip mismatch:\n in=%+v\nout=%+v", in, &out)
	}
}

// TestUsageLogEventCostNonNullDefault — a pre-dispatch 402 (no spend) carries
// cost_usd="0" (NON-NULL, H-1), and an all-zero event still round-trips.
func TestUsageLogEventCostNonNullDefault(t *testing.T) {
	in := &UsageLogEvent{
		HeRequestId: "req_000000000000",
		UserId:      "33333333-3333-3333-3333-333333333333",
		StatusCode:  402,
		CostUsd:     "0",
		ErrorCode:   "402_balance_insufficient",
		Ts:          "2026-06-11T00:00:00Z",
	}
	b, _ := proto.Marshal(in)
	var out UsageLogEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetCostUsd() != "0" || out.GetStatusCode() != 402 || out.GetErrorCode() != "402_balance_insufficient" {
		t.Fatalf("zero-spend event decode mismatch: %+v", &out)
	}
}
