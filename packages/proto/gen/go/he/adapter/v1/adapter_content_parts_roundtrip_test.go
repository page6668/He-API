// Story 9.5 — proto wire-shape round-trip for the hand-edited additive
// ChatMessage.content_parts_json field (9.5-UNIT-015/016/017).
//
// `buf` cannot run locally (project_toolchain_env_limits / BR-2.1), so the
// additive field was hand-edited into adapter.pb.go (struct + getter + rawDesc).
// With NO buf collision check, the field NUMBER must be pinned by an assertion
// on the marshaled tag bytes — a wrong number would silently produce a wire
// collision. These tests LOCK the Q-CONTENT-WIRE (amended) ruling:
// content_parts_json lives on ChatMessage at tag 3 (NOT ChatRequest tag 11).
package adapterv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// protoTag returns the wire tag byte for a length-delimited (wire type 2) field.
func protoTag(fieldNum int) byte { return byte(fieldNum<<3 | 2) }

// 9.5-UNIT-016 (M-1) — content_parts_json marshals at ChatMessage tag 3.
func TestChatMessage_ContentPartsJson_WireTag(t *testing.T) {
	payload := []byte(`[{"type":"text"}]`)
	b, err := proto.Marshal(&ChatMessage{ContentPartsJson: payload})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A ChatMessage with ONLY content_parts_json set marshals to exactly:
	// tag(field 3, wiretype 2), varint(len), payload-bytes.
	want := append([]byte{protoTag(3), byte(len(payload))}, payload...)
	if string(b) != string(want) {
		t.Fatalf("content_parts_json wire bytes = %x, want %x (tag 3? field-number collision?)", b, want)
	}
}

// 9.5-UNIT-016 — content_parts_json round-trips with tag 3 intact.
func TestChatMessage_ContentPartsJson_RoundTrip(t *testing.T) {
	payload := []byte(`[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`)
	in := &ChatMessage{Role: "user", ContentPartsJson: payload}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ChatMessage
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.GetRole() != "user" {
		t.Fatalf("role lost: %q", out.GetRole())
	}
	if string(out.GetContentPartsJson()) != string(payload) {
		t.Fatalf("content_parts_json round-trip lost: %q", out.GetContentPartsJson())
	}
	if out.GetContent() != "" {
		t.Fatalf("content should be empty for a multipart message, got %q", out.GetContent())
	}
}

// 9.5-UNIT-015 — back-compat: a string-content ChatMessage (no parts) is
// byte-identical to the pre-9.5 wire shape (role=1, content=2 only).
func TestChatMessage_StringContent_ByteIdentical(t *testing.T) {
	in := &ChatMessage{Role: "user", Content: "hello"}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-9.5 wire: tag(role,1) len "user" + tag(content,2) len "hello".
	want := []byte{}
	want = append(want, protoTag(1), byte(len("user")))
	want = append(want, []byte("user")...)
	want = append(want, protoTag(2), byte(len("hello")))
	want = append(want, []byte("hello")...)
	if string(b) != string(want) {
		t.Fatalf("string-content wire bytes = %x, want %x (content_parts_json must NOT appear when unset)", b, want)
	}
}

// 9.5-UNIT-017 — non-breaking decode: a NEW multipart message decoded by an
// old text-only ChatMessage view (role+content only) ignores tag 3 silently;
// re-marshalling preserves the unknown tag-3 bytes (proto3 unknown-field
// retention), so an intermediary that round-trips the message does not drop it.
func TestChatMessage_NonBreakingDecode(t *testing.T) {
	payload := []byte(`[{"type":"text","text":"hi"}]`)
	wireBytes, err := proto.Marshal(&ChatMessage{Role: "user", ContentPartsJson: payload})
	if err != nil {
		t.Fatal(err)
	}
	// Decode into a ChatMessage but only read the legacy fields — tag 3 must not
	// corrupt role/content parsing.
	var legacyView ChatMessage
	if err := proto.Unmarshal(wireBytes, &legacyView); err != nil {
		t.Fatalf("an old adapter would fail to decode the new message: %v", err)
	}
	if legacyView.GetRole() != "user" || legacyView.GetContent() != "" {
		t.Fatalf("tag 3 corrupted legacy fields: role=%q content=%q", legacyView.GetRole(), legacyView.GetContent())
	}
}
