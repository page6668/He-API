package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// chatCompletionsPath is the Volcengine Ark v3 OpenAI-compat segment
// appended to DOUBAO_UPSTREAM_BASE_URL (Architect Round 1 OQ-4.5-1:
// `https://ark.cn-beijing.volces.com/api/v3/chat/completions`).
const chatCompletionsPath = "/api/v3/chat/completions"

// TranslateForTest exposes translateRequest to the parent `internal`
// adapter orchestration package + its tests. Named with the `ForTest`
// suffix per Go convention.
func TranslateForTest(ctx context.Context, baseURL, apiKey string, em *EndpointMap, req *adapterv1.ChatRequest) (*http.Request, error) {
	return translateRequest(ctx, baseURL, apiKey, em, req)
}

// translateRequest builds the outbound *http.Request for an upstream
// call per BR-1.7 (a)-(f):
//
//	(a) HTTPS — caller is responsible for the base URL scheme.
//	(b) Authorization: Bearer <apiKey> — OQ-4.5-1 plain Bearer at Ark v3.
//	(c) Content-Type: application/json.
//	(d) Accept: application/json (non-streaming) | text/event-stream (streaming).
//	(e) X-Request-Id: <he_request_id> when set on the proto request.
//	(f) Body: identity-mapped per OQ-4.5-3 for all non-`model` fields; the
//	    `model` field is REWRITTEN from the consumer-facing friendly id
//	    (doubao-pro / doubao-lite) to the Volcengine endpoint id via
//	    EndpointMap.Lookup (returns ErrUnsupportedModel for unset env vars
//	    per BR-1.12 fail-fast).
//
// BR-2.9 invariant: when req.Stream is true, the outbound body's
// stream_options.include_usage is unconditionally true.
func translateRequest(ctx context.Context, baseURL, apiKey string, em *EndpointMap, req *adapterv1.ChatRequest) (*http.Request, error) {
	body, err := buildRequestBody(em, req)
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

// buildRequestBody marshals the proto ChatRequest into the OpenAI / Ark v3
// JSON shape. Identity-mapping for all non-`model` fields per OQ-4.5-3;
// the `model` field is REWRITTEN to the Volcengine endpoint id per BR-1.7.f
// + BR-3.8a (fail-fast on ErrUnsupportedModel per BR-1.12).
func buildRequestBody(em *EndpointMap, req *adapterv1.ChatRequest) ([]byte, error) {
	endpointID, err := em.Lookup(req.GetModel())
	if err != nil {
		return nil, err
	}
	messages := make([]ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ChatMessage{Role: m.Role, Content: m.Content}
	}
	body := ChatRequestJSON{
		Model:    endpointID, // REWRITTEN — friendly id → endpoint id per BR-1.7.f + BR-3.8a
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

// TranslateChatResponse rewrites the non-streaming response `Model` field
// from the Volcengine endpoint id back to the consumer-facing friendly id
// per BR-1.11 inbound back-translate + BR-3.8b. `friendlyModelID` is
// sourced from `req.Model` threaded through `Service.Chat`'s per-request
// closure (Architect Round 1 l-1 ratification: NO ReverseLookup — the
// closure is rotation-safe).
//
// Returns nil if resp is nil. Non-`Model` fields pass through verbatim
// (shallow copy).
func TranslateChatResponse(resp *ChatResponseJSON, friendlyModelID string) *ChatResponseJSON {
	if resp == nil {
		return nil
	}
	out := *resp
	out.Model = friendlyModelID
	return &out
}

// TranslateChatChunk is the streaming companion to TranslateChatResponse —
// applied per-chunk by the Service.Chat streaming loop. REWRITES the
// chunk's `Model` field from the Volcengine endpoint id back to the
// consumer-facing friendly id per BR-1.11 (per-chunk back-translate).
//
// Returns nil if chunk is nil. The rewrite touches `Model` only —
// `Choices`, `Usage`, `ID`, `Object`, `Created` are pass-through.
func TranslateChatChunk(chunk *ChatChunkJSON, friendlyModelID string) *ChatChunkJSON {
	if chunk == nil {
		return nil
	}
	out := *chunk
	out.Model = friendlyModelID
	return &out
}
