package upstream

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// 4.2-UNIT-001 (P0) — OQ-4.2-1 (compat-mode endpoint) + OQ-4.2-6
// (model-family env var naming).
func TestTranslate_BearerAuth_And_CompatModeURL(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "qwen-max",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "sk-qwen-test123", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.URL.String(), "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"; got != want {
		t.Fatalf("URL = %q, want %q (OQ-4.2-1 compat-mode)", got, want)
	}
	if got, want := httpReq.Header.Get("Authorization"), "Bearer sk-qwen-test123"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := httpReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json (non-streaming)", got)
	}
}

// 4.2-UNIT-002 (P0) — BR-1.10 model-passthrough for qwen-max.
func TestTranslate_ModelPassthrough_QwenMax(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "qwen-max",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != "qwen-max" {
		t.Fatalf("body.model = %q, want %q (BR-1.10 verbatim)", got.Model, "qwen-max")
	}
}

// 4.2-UNIT-003 (P0) — BR-1.10 model-passthrough for qwen-plus.
func TestTranslate_ModelPassthrough_QwenPlus(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "qwen-plus",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != "qwen-plus" {
		t.Fatalf("body.model = %q, want %q (BR-1.10 verbatim)", got.Model, "qwen-plus")
	}
}

// 4.2-UNIT-translate-stream-include-usage — BR-2.9 invariant: when
// stream=true, outbound body MUST carry stream_options.include_usage=true
// AND header MUST set Accept: text/event-stream.
func TestTranslate_StreamForcesIncludeUsage_AndAcceptSSE(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "qwen-max",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:   true,
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "k", req)
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

// 4.2-UNIT-translate-request-id — BR-1.5: when req.HeRequestId is set,
// outbound MUST carry X-Request-Id: <id>.
func TestTranslate_PropagatesHeRequestId_AsXRequestId(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:       "qwen-max",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		HeRequestId: "req_a1b2c3d4e5f6",
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.Header.Get("X-Request-Id"), "req_a1b2c3d4e5f6"; got != want {
		t.Fatalf("X-Request-Id = %q, want %q", got, want)
	}
}

// 4.2-UNIT-translate-no-stream-options-when-non-streaming —
// when stream=false, body MUST NOT include stream_options.
func TestTranslate_NonStreaming_OmitsStreamOptions(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "qwen-max",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://dashscope.aliyuncs.com", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("non-streaming body MUST omit stream_options; got %s", body)
	}
}
