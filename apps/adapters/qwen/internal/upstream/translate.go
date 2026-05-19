package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// chatCompletionsPath is the DashScope compat-mode segment appended to
// QWEN_UPSTREAM_BASE_URL (Architect Round 1 OQ-4.2-1: compat-mode endpoint
// `https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions`).
const chatCompletionsPath = "/compatible-mode/v1/chat/completions"

// TranslateForTest exposes translateRequest to the parent `internal`
// adapter orchestration package + its tests. Named with the `ForTest`
// suffix per Go convention.
func TranslateForTest(ctx context.Context, baseURL, apiKey string, req *adapterv1.ChatRequest) (*http.Request, error) {
	return translateRequest(ctx, baseURL, apiKey, req)
}

// translateRequest builds the outbound *http.Request for an upstream
// call per BR-1.7 (a)-(f):
//
//	(a) HTTPS — caller is responsible for the base URL scheme.
//	(b) Authorization: Bearer <apiKey>.
//	(c) Content-Type: application/json.
//	(d) Accept: application/json (non-streaming) | text/event-stream (streaming).
//	(e) X-Request-Id: <he_request_id> when set on the proto request.
//	(f) Body: identity-mapped per OQ-4.2-1 (compat-mode body == OpenAI shape).
//
// BR-2.9 invariant: when req.Stream is true, the outbound body's
// stream_options.include_usage is unconditionally true.
func translateRequest(ctx context.Context, baseURL, apiKey string, req *adapterv1.ChatRequest) (*http.Request, error) {
	body, err := buildRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("build request body: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new http request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if req.HeRequestId != "" {
		httpReq.Header.Set("X-Request-Id", req.HeRequestId)
	}
	return httpReq, nil
}

// buildRequestBody marshals the proto ChatRequest into the OpenAI/Qwen
// compat-mode JSON shape. Identity-mapping per OQ-4.2-1 (compat-mode).
//
// BR-1.10: req.Model is forwarded VERBATIM (the single adapter-qwen
// service routes qwen-max and qwen-plus by upstream's `model` body field).
func buildRequestBody(req *adapterv1.ChatRequest) ([]byte, error) {
	messages := make([]ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ChatMessage{Role: m.Role, Content: m.Content}
	}
	body := ChatRequestJSON{
		Model:    req.Model,
		Messages: messages,
		Stream:   req.Stream,
	}
	if req.Temperature != nil {
		t := req.GetTemperature()
		body.Temperature = &t
	}
	if req.MaxTokens != nil {
		mt := req.GetMaxTokens()
		body.MaxTokens = &mt
	}
	if len(req.ToolsJson) > 0 {
		body.Tools = json.RawMessage(req.ToolsJson)
	}
	if len(req.ToolChoiceJson) > 0 {
		body.ToolChoice = json.RawMessage(req.ToolChoiceJson)
	}
	if len(req.ResponseFormatJson) > 0 {
		body.ResponseFormat = json.RawMessage(req.ResponseFormatJson)
	}
	if req.Stream {
		// BR-2.9 — unconditionally force stream_options.include_usage=true.
		body.StreamOptions = &StreamOptionsJSON{IncludeUsage: true}
	}
	return json.Marshal(body)
}
