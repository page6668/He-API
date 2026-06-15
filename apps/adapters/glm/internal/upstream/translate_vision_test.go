package upstream

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// bodyContentRaw unmarshals the outbound body and returns messages[i].content
// as raw JSON (so a string vs an array can be distinguished verbatim).
func bodyContentRaw(t *testing.T, req *adapterv1.ChatRequest) []json.RawMessage {
	t.Helper()
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	out := make([]json.RawMessage, len(got.Messages))
	for i, m := range got.Messages {
		out[i] = m.Content
	}
	return out
}

// 9.5-UNIT-019 — a multipart message (content_parts_json set) emits the vendor
// `content` as the parts ARRAY verbatim (Zhipu v4 compat-mode near-identity).
func TestTranslate_MultipartContent_EmittedVerbatim(t *testing.T) {
	parts := `[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"https://example.com/cat.jpg"}}]`
	req := &adapterv1.ChatRequest{
		Model:    "glm-4v",
		Messages: []*adapterv1.ChatMessage{{Role: "user", ContentPartsJson: []byte(parts)}},
	}
	got := bodyContentRaw(t, req)
	if len(got) != 1 {
		t.Fatalf("messages len = %d, want 1", len(got))
	}
	var gotParts, wantParts any
	_ = json.Unmarshal(got[0], &gotParts)
	_ = json.Unmarshal([]byte(parts), &wantParts)
	gj, _ := json.Marshal(gotParts)
	wj, _ := json.Marshal(wantParts)
	if string(gj) != string(wj) {
		t.Fatalf("vendor content = %s, want %s", got[0], parts)
	}
}

// 9.5 (back-compat) — a string message emits `content` as the byte-identical
// JSON string (content_parts_json nil → legacy path unchanged).
func TestTranslate_StringContent_ByteIdentical(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "glm-4",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi there"}},
	}
	got := bodyContentRaw(t, req)
	if len(got) != 1 || string(got[0]) != `"Hi there"` {
		t.Fatalf("vendor content = %s, want \"Hi there\" (byte-identical legacy)", got[0])
	}
}
