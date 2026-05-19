package upstream

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// 4.4-UNIT-001 (P0) — OQ-4.4-1 (Zhipu v4 OpenAI-compat endpoint URL) +
// OQ-4.4-2 (GLM_* env-var brand-name naming). The outbound HTTPS request
// MUST target https://open.bigmodel.cn/api/paas/v4/chat/completions with
// plain `Authorization: Bearer` (NOT JWT-signed legacy scheme).
func TestTranslate_BearerAuth_And_ZhipuURL(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "glm-4",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "sk-glm-test123", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.URL.String(), "https://open.bigmodel.cn/api/paas/v4/chat/completions"; got != want {
		t.Fatalf("URL = %q, want %q (OQ-4.4-1 Zhipu v4 endpoint)", got, want)
	}
	if got, want := httpReq.Header.Get("Authorization"), "Bearer sk-glm-test123"; got != want {
		t.Fatalf("Authorization = %q, want %q (plain Bearer per OQ-4.4-1; NOT JWT-signed)", got, want)
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := httpReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json (non-streaming)", got)
	}
}

// 4.4-UNIT-002 (P0) — BR-1.10 N=1 model-passthrough for `glm-4`. The
// outbound HTTPS body's `model` field is the only routing signal at the
// upstream side; adapter MUST forward req.Model verbatim.
func TestTranslate_ModelPassthrough_GLM4(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "glm-4",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != "glm-4" {
		t.Fatalf("body.model = %q, want %q (BR-1.10 verbatim)", got.Model, "glm-4")
	}
}

// 4.4-UNIT-008 — BR-2.9 invariant: when stream=true, outbound body MUST
// carry stream_options.include_usage=true AND header MUST set
// Accept: text/event-stream.
func TestTranslate_StreamForcesIncludeUsage_AndAcceptSSE(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "glm-4",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:   true,
	}
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got := httpReq.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", got)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if !got.Stream {
		t.Fatalf("body.stream = false, want true")
	}
	if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
		t.Fatalf("body.stream_options.include_usage missing or false (BR-2.9): %#v", got.StreamOptions)
	}
}

// 4.4-UNIT-translate-request-id — BR-1.5: when req.HeRequestId is set,
// outbound MUST carry X-Request-Id: <id>.
func TestTranslate_PropagatesHeRequestId_AsXRequestId(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:       "glm-4",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		HeRequestId: "req_a1b2c3d4e5f6",
	}
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.Header.Get("X-Request-Id"), "req_a1b2c3d4e5f6"; got != want {
		t.Fatalf("X-Request-Id = %q, want %q", got, want)
	}
}

// 4.4-UNIT-translate-no-stream-options-when-non-streaming —
// when stream=false, body MUST NOT include stream_options.
func TestTranslate_NonStreaming_OmitsStreamOptions(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "glm-4",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://open.bigmodel.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("non-streaming body MUST omit stream_options; got %s", body)
	}
}
