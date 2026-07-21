package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// chatCompletionsPath is the DeepSeek (OpenAI-compatible) endpoint segment
// appended to DEEPSEEK_UPSTREAM_BASE_URL.
const chatCompletionsPath = "/v1/chat/completions"

// TranslateForTest exposes translateRequest to callers outside the upstream
// package (the parent `internal` adapter orchestration package + its tests).
// Named with the `ForTest` suffix per Go convention: the helper is not
// meant to be part of the package's stable public surface — only the
// adapter Service uses it.
func TranslateForTest(ctx context.Context, baseURL, apiKey string, req *adapterv1.ChatRequest) (*http.Request, error) {
	return translateRequest(ctx, baseURL, apiKey, req)
}

// translateRequest builds the outbound *http.Request for an upstream call,
// per BR-1.7 (a)-(f).
//
//	(a) HTTPS — caller is responsible for the base URL scheme.
//	(b) Authorization: Bearer <apiKey>.
//	(c) Content-Type: application/json.
//	(d) Accept: application/json (non-streaming) | text/event-stream (streaming).
//	(e) X-Request-Id: <he_request_id> when set on the proto request.
//	(f) Body: identity-mapped from the proto ChatRequest (DeepSeek's request
//	    shape matches OpenAI's verbatim). Stories 4.2-4.6 supply non-
//	    identity translations.
//
// BR-2.9 invariant: when req.Stream is true, the outbound body's
// stream_options.include_usage is unconditionally true (so the tail-usage
// SSE chunk is always present regardless of what the gateway forwarded).
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

// upstreamModelID maps the gateway's canonical model id onto the id DeepSeek's
// own API accepts.
//
// BR-1.7 (f) originally assumed identity-mapping, but that assumption does not
// hold against the live vendor API: the gateway advertises `deepseek-v3` while
// api.deepseek.com accepts `deepseek-chat` — passing the catalogue id straight
// through returns "model not found". Unknown ids pass through unchanged so a
// newly added model keeps working without a code change.
//
// DEEPSEEK_MODEL_MAP overrides/extends the table without a redeploy; format is
// "canonical=upstream,canonical=upstream" (e.g. "deepseek-v3=deepseek-chat").
func upstreamModelID(canonical string) string {
	if raw := os.Getenv("DEEPSEEK_MODEL_MAP"); raw != "" {
		for _, pair := range strings.Split(raw, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && strings.TrimSpace(k) == canonical {
				return strings.TrimSpace(v)
			}
		}
	}
	switch canonical {
	case "deepseek-v3":
		return "deepseek-chat"
	case "deepseek-r1":
		return "deepseek-reasoner"
	default:
		return canonical
	}
}

// buildRequestBody marshals the proto ChatRequest into the OpenAI/DeepSeek
// JSON shape, translating the model id to the vendor's own naming.
func buildRequestBody(req *adapterv1.ChatRequest) ([]byte, error) {
	messages := make([]ChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ChatMessage{Role: m.Role, Content: m.Content}
	}
	body := ChatRequestJSON{
		Model:    upstreamModelID(req.Model),
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
		// BR-2.9: unconditionally force stream_options.include_usage=true.
		body.StreamOptions = &StreamOptionsJSON{IncludeUsage: true}
	}
	return json.Marshal(body)
}
