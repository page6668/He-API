// Story 9.6 (T5) — 9.6-UNIT-017. The hand-edited additive
// BILLING_MODE_PER_MINUTE=3 + UsageEvent.audio_duration_seconds=14 must:
// (a) round-trip an ASR UsageEvent losslessly, and (b) leave an existing
// token UsageEvent byte-identical to its pre-9.6 encoding (money wire blast
// radius). `buf` cannot run locally (project_toolchain_env_limits); the
// descriptor was regenerated from the compiled descriptor.
package billingv1

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// 9.6-UNIT-017 — an ASR UsageEvent (PER_MINUTE + audio_duration_seconds, tokens
// 0) round-trips losslessly and byte-stably.
func TestUsageEvent_PerMinute_RoundTrip(t *testing.T) {
	in := &UsageEvent{
		LedgerKey:            "he-req-asr-1",
		HeRequestId:          "he-req-asr-1",
		UserId:               "u-1",
		ApiKeyId:             "ak-1",
		Model:                "doubao-asr",
		BillingMode:          BillingMode_BILLING_MODE_PER_MINUTE,
		AudioDurationSeconds: 3.2,
		Ts:                   "2026-06-15T10:00:00Z",
	}
	b1, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out UsageEvent
	if err := proto.Unmarshal(b1, &out); err != nil {
		t.Fatal(err)
	}
	if out.GetBillingMode() != BillingMode_BILLING_MODE_PER_MINUTE {
		t.Fatalf("billing_mode = %v, want PER_MINUTE", out.GetBillingMode())
	}
	if out.GetAudioDurationSeconds() != 3.2 {
		t.Fatalf("audio_duration_seconds = %v, want 3.2", out.GetAudioDurationSeconds())
	}
	if out.GetPromptTokens() != 0 || out.GetCompletionTokens() != 0 || out.GetTotalTokens() != 0 {
		t.Fatalf("ASR token fields must be 0, got %+v", &out)
	}
	b2, _ := proto.MarshalOptions{Deterministic: true}.Marshal(&out)
	if string(b1) != string(b2) {
		t.Fatalf("re-marshal not byte-stable")
	}
}

// 9.6-UNIT-017 — an existing token UsageEvent (no audio fields) is byte-identical
// to its pre-9.6 encoding. The added field 14 must NOT appear when unset.
func TestUsageEvent_TokenEvent_ByteIdentical(t *testing.T) {
	in := &UsageEvent{
		LedgerKey:        "he-req-1",
		HeRequestId:      "he-req-1",
		Model:            "deepseek-v3",
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
		BillingMode:      BillingMode_BILLING_MODE_PER_TOKEN,
		Ts:               "2026-06-15T10:00:00Z",
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Walk the wire fields; field number 14 (audio_duration_seconds) must be
	// ABSENT from a token event's encoding (proto3 omits the zero scalar).
	rest := b
	for len(rest) > 0 {
		num, _, n := protowire.ConsumeTag(rest)
		if n < 0 {
			t.Fatalf("malformed wire bytes")
		}
		if num == 14 {
			t.Fatalf("audio_duration_seconds (field 14) leaked into a token event: %x", b)
		}
		_, _, fn := protowire.ConsumeField(rest)
		if fn < 0 {
			t.Fatalf("malformed field")
		}
		rest = rest[fn:]
	}
	var out UsageEvent
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.GetAudioDurationSeconds() != 0 {
		t.Fatalf("token event audio_duration_seconds must be 0")
	}
}

// 9.6-UNIT-017 — the enum descriptor carries the additive value and the existing
// values are unperturbed.
func TestBillingMode_EnumDescriptor(t *testing.T) {
	ed := BillingMode_BILLING_MODE_PER_MINUTE.Descriptor()
	v := ed.Values().ByNumber(protoreflect.EnumNumber(3))
	if v == nil || string(v.Name()) != "BILLING_MODE_PER_MINUTE" {
		t.Fatalf("PER_MINUTE=3 missing from enum descriptor")
	}
	// 9.7 added BILLING_MODE_PER_CHARACTER=4 (additive) → 5 values.
	if ed.Values().Len() != 5 {
		t.Fatalf("BillingMode value count = %d, want 5", ed.Values().Len())
	}
	if BillingMode_name[3] != "BILLING_MODE_PER_MINUTE" || BillingMode_value["BILLING_MODE_PER_MINUTE"] != 3 {
		t.Fatalf("BillingMode name/value maps not updated")
	}
}
