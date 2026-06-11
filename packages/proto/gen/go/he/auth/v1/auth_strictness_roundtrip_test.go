// Story 8.4 — proto wire-shape round-trip for the hand-edited additive
// content_safety_strictness fields (8.4-UNIT-047, H-1 collision net).
//
// `buf` cannot run locally (project_toolchain_env_limits / OQ-8.4-7), so the
// additive fields were hand-edited into auth.pb.go (struct + getter + rawDesc).
// With NO buf collision check, the field NUMBERS must be pinned by an assertion
// on the marshaled tag bytes — a wrong number would silently produce a wire
// collision. These tests LOCK the H-1 ruling: Validate=8, UpdateReq=8 (optional),
// UpdateResp=10, Entry=10.
package authv1

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// protoTag returns the wire tag byte for a length-delimited (wire type 2) field.
func protoTag(fieldNum int) byte { return byte(fieldNum<<3 | 2) }

func TestContentSafetyStrictness_WireTags(t *testing.T) {
	const v = "loose"
	cases := []struct {
		name  string
		field int
		bytes func() ([]byte, error)
	}{
		{"ValidateApiKeyResponse", 8, func() ([]byte, error) {
			return proto.Marshal(&ValidateApiKeyResponse{ContentSafetyStrictness: v})
		}},
		{"UpdateApiKeyResponse", 10, func() ([]byte, error) {
			return proto.Marshal(&UpdateApiKeyResponse{ContentSafetyStrictness: v})
		}},
		{"ApiKeyEntry", 10, func() ([]byte, error) {
			return proto.Marshal(&ApiKeyEntry{ContentSafetyStrictness: v})
		}},
		{"UpdateApiKeyRequest", 8, func() ([]byte, error) {
			return proto.Marshal(&UpdateApiKeyRequest{ContentSafetyStrictness: proto.String(v)})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.bytes()
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// A message with ONLY this string field set marshals to exactly
			//: tag, varint(len), value-bytes. Assert the pinned tag + payload.
			want := append([]byte{protoTag(tc.field), byte(len(v))}, []byte(v)...)
			if string(b) != string(want) {
				t.Fatalf("%s field %d: wire bytes = %x, want %x (field-number collision?)",
					tc.name, tc.field, b, want)
			}
		})
	}
}

func TestContentSafetyStrictness_RoundTrip(t *testing.T) {
	// Plain (NOT NULL) read-back fields round-trip the value verbatim.
	t.Run("ValidateApiKeyResponse", func(t *testing.T) {
		in := &ValidateApiKeyResponse{Ok: true, ApiKeyId: "k", ContentSafetyStrictness: "default"}
		b, err := proto.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		var out ValidateApiKeyResponse
		if err := proto.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out.GetContentSafetyStrictness() != "default" {
			t.Fatalf("round-trip lost the field: %q", out.GetContentSafetyStrictness())
		}
	})

	// UpdateApiKeyRequest proto3-optional present/absent semantics.
	t.Run("UpdateApiKeyRequest_present_vs_absent", func(t *testing.T) {
		// Absent (nil) → no wire bytes for the field; getter "".
		absent := &UpdateApiKeyRequest{UserId: "u", ApiKeyId: "k"}
		b, _ := proto.Marshal(absent)
		var roundAbsent UpdateApiKeyRequest
		if err := proto.Unmarshal(b, &roundAbsent); err != nil {
			t.Fatal(err)
		}
		if roundAbsent.ContentSafetyStrictness != nil {
			t.Fatalf("absent field deserialized non-nil: %v", roundAbsent.ContentSafetyStrictness)
		}

		// Present (even empty string) → field carried (the pointer survives).
		present := &UpdateApiKeyRequest{ContentSafetyStrictness: proto.String("strict")}
		b2, _ := proto.Marshal(present)
		var roundPresent UpdateApiKeyRequest
		if err := proto.Unmarshal(b2, &roundPresent); err != nil {
			t.Fatal(err)
		}
		if roundPresent.ContentSafetyStrictness == nil || *roundPresent.ContentSafetyStrictness != "strict" {
			t.Fatalf("present field lost: %v", roundPresent.ContentSafetyStrictness)
		}
	})
}
