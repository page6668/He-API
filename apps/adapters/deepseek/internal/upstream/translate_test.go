package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// 4.1-UNIT-001 (P0) — BR-1.7 (b) + OQ1 anchor.
// translateRequest sets Authorization: Bearer <upstream_api_key> when the
// adapter is configured with DEEPSEEK_UPSTREAM_API_KEY="sk-test123".
func TestTranslateRequest_BearerAuthorization(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "sk-test123",
		&adapterv1.ChatRequest{
			Model:    "deepseek-v3",
			Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Hello"}},
		})
	if err != nil {
		t.Fatalf("translateRequest err = %v, want nil", err)
	}
	if got, want := req.Header.Get("Authorization"), "Bearer sk-test123"; got != want {
		t.Fatalf("Authorization header = %q, want %q", got, want)
	}
}

// 4.1-UNIT-002 (P0) — BR-1.7 (c). Content-Type is application/json.
func TestTranslateRequest_ContentTypeJSON(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{
			Model:    "deepseek-v3",
			Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

// 4.1-UNIT-003 (P0) — BR-1.7 (d) non-streaming. Accept: application/json.
func TestTranslateRequest_Accept_NonStreaming(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{
			Model:    "deepseek-v3",
			Stream:   false,
			Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got, want := req.Header.Get("Accept"), "application/json"; got != want {
		t.Fatalf("Accept (non-streaming) = %q, want %q", got, want)
	}
}

// 4.1-UNIT-004 (P0) — BR-1.7 (d) streaming + BR-2.9 forced inclusion.
// stream=true → Accept: text/event-stream + body carries
// stream_options.include_usage:true even when the request omits it.
//
// NOTE: this test asserts the body shape and Accept header. The streaming
// PATH (server-streaming Chat handler invocation) is implemented in Phase B;
// the request translation itself is identity-set in Phase A so the upstream
// contract is correct from the first byte.
func TestTranslateRequest_Accept_Streaming_ForcesIncludeUsage(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{
			Model:    "deepseek-v3",
			Stream:   true,
			Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got, want := req.Header.Get("Accept"), "text/event-stream"; got != want {
		t.Fatalf("Accept (streaming) = %q, want %q", got, want)
	}
	body, _ := io.ReadAll(req.Body)
	if !bytes.Contains(body, []byte(`"include_usage":true`)) {
		t.Fatalf("body does not include stream_options.include_usage:true: %s", body)
	}
	if !bytes.Contains(body, []byte(`"stream":true`)) {
		t.Fatalf("body does not include stream:true: %s", body)
	}
}

// 4.1-UNIT-005 (P0) — BR-1.7 (e) + BR-1.5 + OQ7 anchor. The propagated
// he_request_id from the gateway is set as X-Request-Id on the upstream
// request (DeepSeek's vendor header convention). HTTP/2 forcing on the
// wire is asserted by the INT-006 live-wire test; UNIT scope verifies the
// Transport is configured for HTTP/2-forced when the client is built.
func TestTranslateRequest_PropagatesRequestID(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{
			Model:        "deepseek-v3",
			Messages:     []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			HeRequestId:  "req_a1b2c3d4e5f6",
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got, want := req.Header.Get("X-Request-Id"), "req_a1b2c3d4e5f6"; got != want {
		t.Fatalf("X-Request-Id = %q, want %q", got, want)
	}
}

// 4.1-UNIT-006 (P0) — BR-1.7 (f) identity-mapping. The body sent to
// DeepSeek faithfully contains the gateway-received fields (model + messages
// + temperature + max_tokens) without restructuring. Identity-mapping is
// DeepSeek-specific (Stories 4.2-4.6 supply non-identity translations).
func TestTranslateRequest_BodyIdentityMapping(t *testing.T) {
	temp := float64(0.7)
	mt := int32(100)
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{
			Model:    "deepseek-v3",
			Messages: []*adapterv1.ChatMessage{{Role: "system", Content: "be terse"}, {Role: "user", Content: "Hi"}},
			Temperature: &temp,
			MaxTokens:   &mt,
		})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	body, _ := io.ReadAll(req.Body)
	var got ChatRequestJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body unmarshal: %v\nraw: %s", err, body)
	}
	if got.Model != "deepseek-v3" {
		t.Fatalf("body.model = %q, want %q", got.Model, "deepseek-v3")
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Content != "Hi" {
		t.Fatalf("body.messages mismatch: %#v", got.Messages)
	}
	if got.Temperature == nil || *got.Temperature != 0.7 {
		t.Fatalf("body.temperature = %v, want 0.7", got.Temperature)
	}
	if got.MaxTokens == nil || *got.MaxTokens != 100 {
		t.Fatalf("body.max_tokens = %v, want 100", got.MaxTokens)
	}
}

// Defensive: translateRequest with empty messages still produces a valid
// body (validation happens upstream of the adapter — the gateway rejects
// empty messages with 400 before the adapter is invoked, per BR-2.1 + the
// existing chat_completions.go validateChatRequest path).
func TestTranslateRequest_EmptyMessagesProducesValidBody(t *testing.T) {
	req, err := translateRequest(context.Background(), "https://api.deepseek.example", "k",
		&adapterv1.ChatRequest{Model: "deepseek-v3"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), `"model":"deepseek-v3"`) {
		t.Fatalf("body missing model: %s", body)
	}
}
