// Story 9.6 (T1) — proto wire-shape round-trip for the hand-edited additive
// `Transcribe` RPC + TranscribeRequest/TranscribeResponse messages.
//
// `buf` cannot run locally (project_toolchain_env_limits / BR-2.1), so the
// additive RPC + messages were hand-edited into adapter.pb.go (structs +
// getters + rawDesc) and adapterv1connect (client/handler). With NO buf
// collision check, these tests LOCK: (a) new-message round-trip byte-stability,
// (b) byte-identical existing ChatRequest/ChatChunk (wire blast radius), and
// (c) a full FileDescriptor parse+marshal after the hand-edit
// (Architect-required, UNIT-010).
package adapterv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// 9.6-UNIT-008 — TranscribeRequest/TranscribeResponse marshal↔unmarshal
// round-trip is byte-stable (deterministic marshal) and lossless.
func TestTranscribeRequest_RoundTrip(t *testing.T) {
	temp := 0.4
	in := &TranscribeRequest{
		Model:          "doubao-asr",
		Audio:          []byte{0xff, 0xfb, 0x90, 0x00, 0x01, 0x02}, // mp3-ish bytes
		MimeType:       "audio/mpeg",
		Language:       "zh",
		Prompt:         "会议纪要",
		ResponseFormat: "verbose_json",
		Temperature:    &temp,
		HeRequestId:    "he-req-abc123",
	}
	b1, err := proto.MarshalOptions{Deterministic: true}.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out TranscribeRequest
	if err := proto.Unmarshal(b1, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.GetModel() != in.Model || out.GetMimeType() != in.MimeType ||
		out.GetLanguage() != in.Language || out.GetPrompt() != in.Prompt ||
		out.GetResponseFormat() != in.ResponseFormat || out.GetHeRequestId() != in.HeRequestId {
		t.Fatalf("string fields lost: %+v", &out)
	}
	if string(out.GetAudio()) != string(in.Audio) {
		t.Fatalf("audio bytes lost: %x", out.GetAudio())
	}
	if out.Temperature == nil || out.GetTemperature() != temp {
		t.Fatalf("temperature (proto3 optional) lost: %v", out.Temperature)
	}
	// Byte-stable re-marshal.
	b2, err := proto.MarshalOptions{Deterministic: true}.Marshal(&out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("re-marshal not byte-stable:\n %x\n %x", b1, b2)
	}
}

// 9.6-UNIT-008 — TranscribeRequest with temperature UNSET omits field 7 (proto3
// optional presence), and segments_json absent on the response omits field 4.
func TestTranscribe_OptionalPresence(t *testing.T) {
	req := &TranscribeRequest{Model: "doubao-asr"} // temperature unset
	if req.Temperature != nil {
		t.Fatal("temperature should be nil when unset")
	}
	b, _ := proto.Marshal(req)
	var out TranscribeRequest
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Temperature != nil {
		t.Fatalf("temperature must stay absent across round-trip, got %v", *out.Temperature)
	}

	resp := &TranscribeResponse{Text: "你好世界", Language: "zh", DurationSeconds: 3.2}
	rb, err := proto.MarshalOptions{Deterministic: true}.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var rout TranscribeResponse
	if err := proto.Unmarshal(rb, &rout); err != nil {
		t.Fatal(err)
	}
	if rout.GetText() != "你好世界" || rout.GetLanguage() != "zh" || rout.GetDurationSeconds() != 3.2 {
		t.Fatalf("response round-trip lost: %+v", &rout)
	}
	if rout.GetSegmentsJson() != nil {
		t.Fatalf("segments_json must stay absent when unset, got %x", rout.GetSegmentsJson())
	}
}

// 9.6-UNIT-009 — the additive RPC + messages must NOT perturb the existing
// ChatRequest/ChatChunk wire shape. A representative ChatRequest/ChatChunk
// round-trips byte-identically to its pre-9.6 encoding (field tags unchanged).
func TestExistingChatShape_ByteIdentical(t *testing.T) {
	temp := 0.7
	maxTok := int32(128)
	req := &ChatRequest{
		Model:       "deepseek-v3",
		Messages:    []*ChatMessage{{Role: "user", Content: "hello"}},
		Stream:      true,
		Temperature: &temp,
		MaxTokens:   &maxTok,
		HeRequestId: "he-req-1",
	}
	b1, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// Hand-computed pre-9.6 wire bytes for this exact ChatRequest. If a later
	// edit shifts any existing tag, this golden mismatches.
	want := []byte{
		0x0a, 0x0b, 'd', 'e', 'e', 'p', 's', 'e', 'e', 'k', '-', 'v', '3', // 1: model
		0x12, 0x0d, 0x0a, 0x04, 'u', 's', 'e', 'r', 0x12, 0x05, 'h', 'e', 'l', 'l', 'o', // 2: messages{role,content}
		0x18, 0x01, // 3: stream=true
		0x21, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0xe6, 0x3f, // 4: temperature=0.7 (fixed64)
		0x28, 0x80, 0x01, // 5: max_tokens=128
		0x4a, 0x08, 'h', 'e', '-', 'r', 'e', 'q', '-', '1', // 9: he_request_id
	}
	if string(b1) != string(want) {
		t.Fatalf("ChatRequest wire shape drifted:\n got  %x\n want %x", b1, want)
	}

	chunk := &ChatChunk{Id: "chatcmpl-x", Object: "chat.completion", Model: "deepseek-v3"}
	cb1, _ := proto.MarshalOptions{Deterministic: true}.Marshal(chunk)
	var cout ChatChunk
	if err := proto.Unmarshal(cb1, &cout); err != nil {
		t.Fatal(err)
	}
	if cout.GetId() != "chatcmpl-x" || cout.GetObject() != "chat.completion" {
		t.Fatalf("ChatChunk round-trip lost: %+v", &cout)
	}
}

// 9.6-UNIT-010 — Architect-required: a FULL FileDescriptor parse + marshal of
// adapter.proto after the hand-edit. Validates the rawDesc bytes form a valid,
// self-consistent descriptor (TypeBuilder.Build already ran at init; here we
// assert the structural contract the hand-edit was supposed to add).
func TestFileDescriptor_AfterHandEdit(t *testing.T) {
	fd := File_he_adapter_v1_adapter_proto
	if fd == nil {
		t.Fatal("FileDescriptor nil — init failed")
	}
	// Round-trip the descriptor itself through descriptorpb.
	fdp := protodesc.ToFileDescriptorProto(fd)
	raw, err := proto.Marshal(fdp)
	if err != nil {
		t.Fatalf("descriptor marshal: %v", err)
	}
	var back descriptorpb.FileDescriptorProto
	if err := proto.Unmarshal(raw, &back); err != nil {
		t.Fatalf("descriptor unmarshal: %v", err)
	}

	msgs := fd.Messages()
	if msgs.Len() != 8 {
		t.Fatalf("message count = %d, want 8 (6 existing + 2 new)", msgs.Len())
	}
	for _, name := range []string{"ChatRequest", "ChatMessage", "ChatChunk", "Choice", "Delta", "Usage", "TranscribeRequest", "TranscribeResponse"} {
		if msgs.ByName(protoreflect.Name(name)) == nil {
			t.Errorf("message %q missing from descriptor", name)
		}
	}

	svc := fd.Services().ByName("AdapterService")
	if svc == nil {
		t.Fatal("AdapterService missing")
	}
	if svc.Methods().Len() != 2 {
		t.Fatalf("method count = %d, want 2 (Chat + Transcribe)", svc.Methods().Len())
	}
	m := svc.Methods().ByName("Transcribe")
	if m == nil {
		t.Fatal("Transcribe method missing")
	}
	if m.IsStreamingClient() || m.IsStreamingServer() {
		t.Errorf("Transcribe must be unary, got streaming client=%v server=%v", m.IsStreamingClient(), m.IsStreamingServer())
	}
	if got := string(m.Input().FullName()); got != "he.adapter.v1.TranscribeRequest" {
		t.Errorf("Transcribe input = %q", got)
	}
	if got := string(m.Output().FullName()); got != "he.adapter.v1.TranscribeResponse" {
		t.Errorf("Transcribe output = %q", got)
	}
	// Chat must remain server-streaming (unperturbed).
	chat := svc.Methods().ByName("Chat")
	if chat == nil || !chat.IsStreamingServer() {
		t.Errorf("Chat must remain server-streaming")
	}

	// TranscribeRequest field contract.
	tr := msgs.ByName("TranscribeRequest")
	wantFields := map[protoreflect.Name]protoreflect.Kind{
		"model": protoreflect.StringKind, "audio": protoreflect.BytesKind,
		"mime_type": protoreflect.StringKind, "language": protoreflect.StringKind,
		"prompt": protoreflect.StringKind, "response_format": protoreflect.StringKind,
		"temperature": protoreflect.DoubleKind, "he_request_id": protoreflect.StringKind,
	}
	for fname, kind := range wantFields {
		f := tr.Fields().ByName(fname)
		if f == nil {
			t.Errorf("TranscribeRequest.%s missing", fname)
			continue
		}
		if f.Kind() != kind {
			t.Errorf("TranscribeRequest.%s kind = %v, want %v", fname, f.Kind(), kind)
		}
	}
	if f := tr.Fields().ByName("temperature"); f != nil && !f.HasOptionalKeyword() {
		t.Error("TranscribeRequest.temperature must be proto3 optional")
	}
}
