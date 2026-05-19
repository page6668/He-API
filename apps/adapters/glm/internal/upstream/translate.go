package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// chatCompletionsPath is the Zhipu v4 OpenAI-compat segment appended to
// GLM_UPSTREAM_BASE_URL (Architect Round 1 OQ-4.4-1 + OQ-4.4-3: v4
// OpenAI-compatible endpoint `https://open.bigmodel.cn/api/paas/v4/chat/completions`).
const chatCompletionsPath = "/api/paas/v4/chat/completions"

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
//	(b) Authorization: Bearer <apiKey> — OQ-4.4-1 plain Bearer at v4.
//	(c) Content-Type: application/json.
//	(d) Accept: application/json (non-streaming) | text/event-stream (streaming).
//	(e) X-Request-Id: <he_request_id> when set on the proto request.
//	(f) Body: identity-mapped per OQ-4.4-3 (Zhipu v4 body == OpenAI shape).
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

// buildRequestBody marshals the proto ChatRequest into the OpenAI/Zhipu
// v4 JSON shape. Identity-mapping per OQ-4.4-3.
//
// BR-1.10: req.Model is forwarded VERBATIM (the single adapter-glm
// service routes `glm-4` by upstream's `model` body field).
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
