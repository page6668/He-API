// Story 9.7 (T1) — proto wire-shape round-trip for the hand-edited additive
// `Synthesize` RPC + SynthesizeRequest/SynthesizeResponse messages (the MIRROR
// of 9.6's `Transcribe`, audio on the RESPONSE).
//
// `buf` cannot run locally (project_toolchain_env_limits / BR-2.1): the additive
// RPC + messages were hand-edited into adapter.pb.go (structs + getters +
// rawDesc regenerated via cmd/descgen) and adapterv1connect (client/handler).
// With NO buf collision check, these tests LOCK: (a) new-message round-trip
// byte-stability (9.7-UNIT-010), (b) byte-identical existing Transcribe/Chat
// wire shape — the additive RPC perturbs nothing (9.7-UNIT-011), and (c) the
// Synthesize method/messages are present + correct in the FileDescriptor
// (9.7-UNIT-012).
package adapterv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// 9.7-UNIT-010 — SynthesizeRequest/SynthesizeResponse marshal↔unmarshal
// round-trip is byte-stable (deterministic marshal) and lossless, including the
// proto3-optional `speed`.
func TestSynthesizeRequest_RoundTrip(t *testing.T) {
	speed := 1.25
	in := &SynthesizeRequest{
		Model:          "doubao-tts",
		Input:          "你好世界",
		Voice:          "zh_female_1",
		ResponseFormat: "mp3",
		Speed:          &speed,
		HeRequestId:    "he-req-tts-1",
	}
	b1, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SynthesizeRequest
	if err := proto.Unmarshal(b1, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetModel() != in.Model || out.GetInput() != in.Input ||
		out.GetVoice() != in.Voice || out.GetResponseFormat() != in.ResponseFormat ||
		out.GetHeRequestId() != in.HeRequestId {
		t.Fatalf("string fields lost: %+v", &out)
	}
	if out.Speed == nil || out.GetSpeed() != speed {
		t.Fatalf("speed (proto3 optional) lost: %v", out.Speed)
	}
	b2, err := proto.MarshalOptions{Deterministic: true}.Marshal(&out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("re-marshal not byte-stable:\n %x\n %x", b1, b2)
	}

	// Response carries audio inline + mime_type.
	resp := &SynthesizeResponse{Audio: []byte{0xff, 0xfb, 0x90, 0x00, 0x13, 0x37}, MimeType: "audio/mpeg"}
	rb, err := proto.MarshalOptions{Deterministic: true}.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var rout SynthesizeResponse
	if err := proto.Unmarshal(rb, &rout); err != nil {
		t.Fatal(err)
	}
	if string(rout.GetAudio()) != string(resp.Audio) || rout.GetMimeType() != "audio/mpeg" {
		t.Fatalf("response round-trip lost: %+v", &rout)
	}
}

// 9.7-UNIT-010 — `speed` UNSET omits field 5 (proto3-optional presence).
func TestSynthesize_OptionalSpeedPresence(t *testing.T) {
	req := &SynthesizeRequest{Model: "doubao-tts", Input: "hi", Voice: "zh_female_1", ResponseFormat: "mp3"} // speed unset
	if req.Speed != nil {
		t.Fatal("speed should be nil when unset")
	}
	b, _ := proto.Marshal(req)
	var out SynthesizeRequest
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Speed != nil {
		t.Fatalf("speed must stay absent across round-trip, got %v", *out.Speed)
	}
}

// 9.7-UNIT-011 — the additive Synthesize RPC + messages must NOT perturb the
// existing Transcribe wire shape. A representative TranscribeRequest round-trips
// byte-identically to its pre-9.7 encoding (the 9.6 golden), proving the
// rawDesc + struct edits are append-only. (Chat byte-identity is locked by
// TestExistingChatShape_ByteIdentical in the sibling 9.6 test file.)
func TestExistingTranscribeShape_ByteIdentical(t *testing.T) {
	temp := 0.4
	in := &TranscribeRequest{
		Model:          "doubao-asr",
		Audio:          []byte{0x01, 0x02, 0x03},
		MimeType:       "audio/mpeg",
		Language:       "zh",
		ResponseFormat: "json",
		Temperature:    &temp,
		HeRequestId:    "he-req-asr-1",
	}
	got, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x0a, 0x0a, 'd', 'o', 'u', 'b', 'a', 'o', '-', 'a', 's', 'r', // 1: model
		0x12, 0x03, 0x01, 0x02, 0x03, // 2: audio
		0x1a, 0x0a, 'a', 'u', 'd', 'i', 'o', '/', 'm', 'p', 'e', 'g', // 3: mime_type
		0x22, 0x02, 'z', 'h', // 4: language
		0x32, 0x04, 'j', 's', 'o', 'n', // 6: response_format
		0x39, 0x9a, 0x99, 0x99, 0x99, 0x99, 0x99, 0xd9, 0x3f, // 7: temperature=0.4 (fixed64)
		0x42, 0x0c, 'h', 'e', '-', 'r', 'e', 'q', '-', 'a', 's', 'r', '-', '1', // 8: he_request_id
	}
	if string(got) != string(want) {
		t.Fatalf("TranscribeRequest wire shape drifted (9.7 additive edit perturbed 9.6):\n got  %x\n want %x", got, want)
	}
}

// 9.7-UNIT-012 — the FileDescriptor exposes the Synthesize method (unary, correct
// in/out) and the two new messages with the ratified field contract.
func TestSynthesize_FileDescriptor(t *testing.T) {
	fd := File_he_adapter_v1_adapter_proto
	svc := fd.Services().ByName("AdapterService")
	m := svc.Methods().ByName("Synthesize")
	if m == nil {
		t.Fatal("Synthesize method missing")
	}
	if m.IsStreamingClient() || m.IsStreamingServer() {
		t.Errorf("Synthesize must be unary, got client=%v server=%v", m.IsStreamingClient(), m.IsStreamingServer())
	}
	if got := string(m.Input().FullName()); got != "he.adapter.v1.SynthesizeRequest" {
		t.Errorf("Synthesize input = %q", got)
	}
	if got := string(m.Output().FullName()); got != "he.adapter.v1.SynthesizeResponse" {
		t.Errorf("Synthesize output = %q", got)
	}

	req := fd.Messages().ByName("SynthesizeRequest")
	if req == nil {
		t.Fatal("SynthesizeRequest missing")
	}
	wantReq := map[protoreflect.Name]protoreflect.Kind{
		"model": protoreflect.StringKind, "input": protoreflect.StringKind,
		"voice": protoreflect.StringKind, "response_format": protoreflect.StringKind,
		"speed": protoreflect.DoubleKind, "he_request_id": protoreflect.StringKind,
	}
	for fname, kind := range wantReq {
		f := req.Fields().ByName(fname)
		if f == nil {
			t.Errorf("SynthesizeRequest.%s missing", fname)
			continue
		}
		if f.Kind() != kind {
			t.Errorf("SynthesizeRequest.%s kind = %v, want %v", fname, f.Kind(), kind)
		}
	}
	if f := req.Fields().ByName("speed"); f != nil && !f.HasOptionalKeyword() {
		t.Error("SynthesizeRequest.speed must be proto3 optional")
	}

	resp := fd.Messages().ByName("SynthesizeResponse")
	if resp == nil {
		t.Fatal("SynthesizeResponse missing")
	}
	if f := resp.Fields().ByName("audio"); f == nil || f.Kind() != protoreflect.BytesKind {
		t.Error("SynthesizeResponse.audio must be bytes")
	}
	if f := resp.Fields().ByName("mime_type"); f == nil || f.Kind() != protoreflect.StringKind {
		t.Error("SynthesizeResponse.mime_type must be string")
	}
	// Low-1: no character_count field (gateway is the sole rune-count authority).
	if f := resp.Fields().ByName("character_count"); f != nil {
		t.Error("SynthesizeResponse.character_count must NOT exist (BR-4.5 / Architect Low-1)")
	}
}
