package upstream

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// 4.3-UNIT-001 (P0) — OQ-4.3-1 (Moonshot OpenAI-compatible endpoint URL)
// + OQ-4.3-2 (KIMI_* env-var brand-name naming).
func TestTranslate_BearerAuth_And_MoonshotURL(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://api.moonshot.cn", "sk-kimi-test123", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.URL.String(), "https://api.moonshot.cn/v1/chat/completions"; got != want {
		t.Fatalf("URL = %q, want %q (OQ-4.3-1 Moonshot endpoint)", got, want)
	}
	if got, want := httpReq.Header.Get("Authorization"), "Bearer sk-kimi-test123"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if got := httpReq.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := httpReq.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json (non-streaming)", got)
	}
}

// 4.3-UNIT-002 (P0) — BR-1.10 model-passthrough for moonshot-v1-8k.
func TestTranslate_ModelPassthrough_MoonshotV1_8k(t *testing.T) {
	assertModelPassthrough(t, "moonshot-v1-8k")
}

// 4.3-UNIT-003 (P0) — BR-1.10 model-passthrough for moonshot-v1-32k.
func TestTranslate_ModelPassthrough_MoonshotV1_32k(t *testing.T) {
	assertModelPassthrough(t, "moonshot-v1-32k")
}

// 4.3-UNIT-003b (P0) — BR-1.10 model-passthrough for moonshot-v1-128k
// (third size variant; M2 endpoint-dedup N=3 scaling target).
func TestTranslate_ModelPassthrough_MoonshotV1_128k(t *testing.T) {
	assertModelPassthrough(t, "moonshot-v1-128k")
}

func assertModelPassthrough(t *testing.T, model string) {
	t.Helper()
	req := &adapterv1.ChatRequest{
		Model:    model,
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://api.moonshot.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal err = %v; raw = %s", err, body)
	}
	if got.Model != model {
		t.Fatalf("body.model = %q, want %q (BR-1.10 verbatim)", got.Model, model)
	}
}

// 4.3-UNIT-009 — BR-2.9 invariant: when stream=true, outbound body MUST
// carry stream_options.include_usage=true AND header MUST set
// Accept: text/event-stream.
func TestTranslate_StreamForcesIncludeUsage_AndAcceptSSE(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "moonshot-v1-128k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:   true,
	}
	httpReq, err := translateRequest(context.Background(), "https://api.moonshot.cn", "k", req)
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

// 4.3-UNIT-translate-request-id — BR-1.5: when req.HeRequestId is set,
// outbound MUST carry X-Request-Id: <id>.
func TestTranslate_PropagatesHeRequestId_AsXRequestId(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:       "moonshot-v1-8k",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		HeRequestId: "req_a1b2c3d4e5f6",
	}
	httpReq, err := translateRequest(context.Background(), "https://api.moonshot.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	if got, want := httpReq.Header.Get("X-Request-Id"), "req_a1b2c3d4e5f6"; got != want {
		t.Fatalf("X-Request-Id = %q, want %q", got, want)
	}
}

// 4.3-UNIT-translate-no-stream-options-when-non-streaming —
// when stream=false, body MUST NOT include stream_options.
func TestTranslate_NonStreaming_OmitsStreamOptions(t *testing.T) {
	req := &adapterv1.ChatRequest{
		Model:    "moonshot-v1-8k",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
	}
	httpReq, err := translateRequest(context.Background(), "https://api.moonshot.cn", "k", req)
	if err != nil {
		t.Fatalf("translateRequest err = %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("non-streaming body MUST omit stream_options; got %s", body)
	}
}
