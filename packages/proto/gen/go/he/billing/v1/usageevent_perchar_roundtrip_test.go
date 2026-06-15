// Story 9.7 (T5) — 9.7-UNIT-024. The hand-edited additive
// BILLING_MODE_PER_CHARACTER=4 + UsageEvent.character_count=15 must:
// (a) round-trip a TTS UsageEvent losslessly, and (b) leave existing token + ASR
// UsageEvents byte-identical to their pre-9.7 encoding (money wire blast radius).
// `buf` cannot run locally (project_toolchain_env_limits); the descriptor was
// regenerated from the compiled descriptor (cmd/descgenbilling).
package billingv1

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// 9.7-UNIT-024 — a TTS UsageEvent (PER_CHARACTER + character_count, tokens +
// duration 0) round-trips losslessly and byte-stably.
func TestUsageEvent_PerCharacter_RoundTrip(t *testing.T) {
	in := &UsageEvent{
		LedgerKey:      "he-req-tts-1",
		HeRequestId:    "he-req-tts-1",
		UserId:         "u-1",
		ApiKeyId:       "ak-1",
		Model:          "doubao-tts",
		BillingMode:    BillingMode_BILLING_MODE_PER_CHARACTER,
		CharacterCount: 4,
		Ts:             "2026-06-15T10:00:00Z",
	}
	b1, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out UsageEvent
	if err := proto.Unmarshal(b1, &out); err != nil {
		t.Fatal(err)
	}
	if out.GetBillingMode() != BillingMode_BILLING_MODE_PER_CHARACTER {
		t.Fatalf("billing_mode = %v, want PER_CHARACTER", out.GetBillingMode())
	}
	if out.GetCharacterCount() != 4 {
		t.Fatalf("character_count = %v, want 4", out.GetCharacterCount())
	}
	if out.GetPromptTokens() != 0 || out.GetCompletionTokens() != 0 || out.GetTotalTokens() != 0 || out.GetAudioDurationSeconds() != 0 {
		t.Fatalf("TTS token/duration fields must be 0, got %+v", &out)
	}
	b2, _ := proto.MarshalOptions{Deterministic: true}.Marshal(&out)
	if string(b1) != string(b2) {
		t.Fatalf("re-marshal not byte-stable")
	}
}

// 9.7-UNIT-024 — an existing token UsageEvent is byte-identical to its pre-9.7
// encoding: the added field 15 (character_count) must be ABSENT when unset, AND
// field 14 (audio_duration_seconds, 9.6) must remain absent too.
func TestUsageEvent_TokenEvent_ByteIdentical_Post97(t *testing.T) {
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
	rest := b
	for len(rest) > 0 {
		num, _, n := protowire.ConsumeTag(rest)
		if n < 0 {
			t.Fatalf("malformed wire bytes")
		}
		if num == 14 || num == 15 {
			t.Fatalf("audio field %d leaked into a token event: %x", num, b)
		}
		_, _, fn := protowire.ConsumeField(rest)
		if fn < 0 {
			t.Fatalf("malformed field")
		}
		rest = rest[fn:]
	}
}

// 9.7-UNIT-024 — the enum descriptor carries the additive PER_CHARACTER value
// and the existing values are unperturbed.
func TestBillingMode_PerCharacter_EnumDescriptor(t *testing.T) {
	ed := BillingMode_BILLING_MODE_PER_CHARACTER.Descriptor()
	v := ed.Values().ByNumber(protoreflect.EnumNumber(4))
	if v == nil || string(v.Name()) != "BILLING_MODE_PER_CHARACTER" {
		t.Fatalf("PER_CHARACTER=4 missing from enum descriptor")
	}
	// existing PER_MINUTE=3 unperturbed.
	if pm := ed.Values().ByNumber(protoreflect.EnumNumber(3)); pm == nil || string(pm.Name()) != "BILLING_MODE_PER_MINUTE" {
		t.Fatal("PER_MINUTE=3 perturbed by the additive edit")
	}
	if BillingMode_name[4] != "BILLING_MODE_PER_CHARACTER" || BillingMode_value["BILLING_MODE_PER_CHARACTER"] != 4 {
		t.Fatal("BillingMode name/value maps not updated for PER_CHARACTER")
	}
}
