// Story 9.5 — internal unit tests for the Vision multipart ingestion + image
// security helpers (AC1 / AC3). In-package (handlers) so unexported helpers
// (ChatMessage.UnmarshalJSON, validateChatRequest, validateVisionParts,
// validateImageURL, requestHasImage, buildAdapterRequest) are directly testable.
//
// Scenario IDs: docs/qa/assessments/9.5-test-design-20260615.md.
package handlers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// 9.5-UNIT-001 — string content (legacy) → Content set, Parts nil (byte-identical).
func TestChatMessage_UnmarshalJSON_StringContent(t *testing.T) {
	var m ChatMessage
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Content != "hello" || m.Parts != nil {
		t.Fatalf("string content: Content=%q Parts=%v, want Content=\"hello\" Parts=nil", m.Content, m.Parts)
	}
}

// 9.5-UNIT-002 — array content → Parts set, Content "".
func TestChatMessage_UnmarshalJSON_ArrayContent(t *testing.T) {
	var m ChatMessage
	body := `{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x/y.png","detail":"low"}}]}`
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Content != "" || len(m.Parts) != 2 {
		t.Fatalf("array content: Content=%q len(Parts)=%d, want \"\"/2", m.Content, len(m.Parts))
	}
	if m.Parts[0].Type != partTypeText || m.Parts[0].Text != "hi" {
		t.Errorf("part[0] = %+v", m.Parts[0])
	}
	if m.Parts[1].Type != partTypeImageURL || m.Parts[1].ImageURL == nil || m.Parts[1].ImageURL.URL != "https://x/y.png" || m.Parts[1].ImageURL.Detail != "low" {
		t.Errorf("part[1] = %+v", m.Parts[1])
	}
}

// 9.5-UNIT-003 — non-string/non-array content (object/number/bool) → errContentShape.
func TestChatMessage_UnmarshalJSON_BadShape(t *testing.T) {
	for _, body := range []string{
		`{"role":"user","content":{"foo":1}}`,
		`{"role":"user","content":42}`,
		`{"role":"user","content":true}`,
	} {
		var m ChatMessage
		err := json.Unmarshal([]byte(body), &m)
		if !errors.Is(err, errContentShape) {
			t.Errorf("body %s: err = %v, want errContentShape", body, err)
		}
	}
}

// 9.5-UNIT-010 (M-2) — validateChatRequest ACCEPTS a multipart message
// (Content=="" but len(Parts)>0).
func TestValidateChatRequest_AcceptsMultipart(t *testing.T) {
	req := &ChatRequest{
		Model: "qwen-vl-max",
		Messages: []ChatMessage{{
			Role:  "user",
			Parts: []ContentPart{{Type: partTypeText, Text: "hi"}},
		}},
	}
	if _, _, _, ok := validateChatRequest(req); !ok {
		t.Fatalf("validateChatRequest rejected a valid multipart message (M-2 regression)")
	}
}

// 9.5-UNIT-011 — validateChatRequest REJECTS a message with neither content nor parts.
func TestValidateChatRequest_RejectsEmptyMessage(t *testing.T) {
	req := &ChatRequest{
		Model:    "qwen-max",
		Messages: []ChatMessage{{Role: "user"}}, // no content, no parts
	}
	if _, _, _, ok := validateChatRequest(req); ok {
		t.Fatalf("validateChatRequest accepted an empty message (no content, no parts)")
	}
}

// 9.5-UNIT-006 — unknown content-part type → 400.
func TestValidateVisionParts_UnknownType(t *testing.T) {
	req := &ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: []ContentPart{{Type: "video"}}}}}
	status, code, msg, ok := validateVisionParts(req)
	if ok || status != 400 || code != "400_invalid_request" || !strings.Contains(msg, "Unsupported content part type 'video'") {
		t.Fatalf("unknown type: status=%d code=%s msg=%q ok=%v", status, code, msg, ok)
	}
}

// 9.5-UNIT-034 — too many images → 400.
func TestValidateVisionParts_TooManyImages(t *testing.T) {
	parts := make([]ContentPart, 0, maxImagesPerRequest+1)
	for i := 0; i <= maxImagesPerRequest; i++ {
		parts = append(parts, ContentPart{Type: partTypeImageURL, ImageURL: &ImageURL{URL: "https://example.com/x.png"}})
	}
	req := &ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: parts}}}
	_, _, msg, ok := validateVisionParts(req)
	if ok || !strings.Contains(msg, "Too many images") {
		t.Fatalf("too many images: msg=%q ok=%v", msg, ok)
	}
}

// 9.5-UNIT-006b — image part with missing url → 400 (path-only message, no PII).
func TestValidateVisionParts_MissingURL(t *testing.T) {
	req := &ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: []ContentPart{{Type: partTypeImageURL, ImageURL: &ImageURL{}}}}}}
	_, _, msg, ok := validateVisionParts(req)
	if ok || !strings.Contains(msg, "messages[0].content[0].image_url.url") {
		t.Fatalf("missing url: msg=%q ok=%v", msg, ok)
	}
}

// 9.5-UNIT-028/029/030/033/035/036 — validateImageURL SSRF + limits matrix.
func TestValidateImageURL_Matrix(t *testing.T) {
	// A valid ~small base64 png payload.
	smallPNG := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not-really-a-png-but-small"))
	// An oversized (>4 MiB decoded) base64 image.
	big := make([]byte, maxImageBytes+1)
	bigB64 := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(big)

	cases := []struct {
		name   string
		url    string
		wantOK bool
	}{
		{"https remote ok", "https://example.com/cat.jpg", true},
		{"http rejected (UNIT-028)", "http://example.com/cat.jpg", false},
		{"file rejected (UNIT-029)", "file:///etc/passwd", false},
		{"ftp rejected", "ftp://host/x", false},
		{"gopher rejected", "gopher://host/x", false},
		{"metadata IP rejected (UNIT-030)", "https://169.254.169.254/latest/meta-data/", false},
		{"private 10/8 rejected", "https://10.0.0.1/x", false},
		{"loopback rejected", "https://127.0.0.1/x", false},
		{"private 192.168 rejected", "https://192.168.1.1/x", false},
		{"ipv6 loopback rejected", "https://[::1]/x", false},
		{"data png ok", smallPNG, true},
		{"data non-image rejected (UNIT-033)", "data:text/html;base64,PHNjcmlwdD4=", false},
		{"data bmp rejected (UNIT-036)", "data:image/bmp;base64,Qk0=", false},
		{"data oversize rejected (UNIT-035)", bigB64, false},
		{"data missing base64 marker", "data:image/png,rawtext", false},
	}
	for _, c := range cases {
		_, ok := validateImageURL(c.url)
		if ok != c.wantOK {
			t.Errorf("%s: validateImageURL ok=%v, want %v", c.name, ok, c.wantOK)
		}
	}
}

// 9.5-UNIT-031 (PII) — a rejected image_url's error message NEVER contains the URL.
func TestValidateImageURL_NoURLLeak(t *testing.T) {
	const secret = "http://169.254.169.254/latest/meta-data/iam/security-credentials/"
	msg, ok := validateImageURL(secret)
	if ok {
		t.Fatal("expected rejection")
	}
	if strings.Contains(msg, "169.254.169.254") || strings.Contains(msg, "meta-data") {
		t.Fatalf("error message leaked the URL: %q", msg)
	}
}

// 9.5-UNIT-018 — buildAdapterRequest sets EXACTLY ONE of content / content_parts_json
// per message (BR-2.2).
func TestBuildAdapterRequest_OneOfPerMessage(t *testing.T) {
	req := &ChatRequest{
		Model: "qwen-vl-max",
		Messages: []ChatMessage{
			{Role: "system", Content: "be terse"},
			{Role: "user", Parts: []ContentPart{
				{Type: partTypeText, Text: "what is this"},
				{Type: partTypeImageURL, ImageURL: &ImageURL{URL: "https://example.com/cat.jpg"}},
			}},
		},
	}
	out := buildAdapterRequest(req, "qwen-vl-max", "req_test")
	// message 0 — string: content set, content_parts_json nil.
	if out.Messages[0].Content != "be terse" || out.Messages[0].ContentPartsJson != nil {
		t.Fatalf("msg0: content=%q parts=%v, want string-only", out.Messages[0].Content, out.Messages[0].ContentPartsJson)
	}
	// message 1 — multipart: content "" + content_parts_json holds the parts JSON.
	if out.Messages[1].Content != "" || len(out.Messages[1].ContentPartsJson) == 0 {
		t.Fatalf("msg1: content=%q parts=%s, want multipart", out.Messages[1].Content, out.Messages[1].ContentPartsJson)
	}
	var parts []ContentPart
	if err := json.Unmarshal(out.Messages[1].ContentPartsJson, &parts); err != nil {
		t.Fatalf("content_parts_json is not valid JSON: %v", err)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/cat.jpg" {
		t.Fatalf("content_parts_json round-trip mismatch: %+v", parts)
	}
}

// 9.5 — requestHasImage detects an image_url across any message.
func TestRequestHasImage(t *testing.T) {
	noImg := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}, {Role: "user", Parts: []ContentPart{{Type: partTypeText, Text: "x"}}}}}
	if requestHasImage(noImg) {
		t.Error("requestHasImage = true for a text-only request")
	}
	withImg := &ChatRequest{Messages: []ChatMessage{{Role: "user", Parts: []ContentPart{{Type: partTypeImageURL, ImageURL: &ImageURL{URL: "https://x/y.png"}}}}}}
	if !requestHasImage(withImg) {
		t.Error("requestHasImage = false for a request with an image part")
	}
}
