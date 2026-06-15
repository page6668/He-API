// Package upstream owns the GLM Zhipu HTTPS-API wire types and the
// HTTP/2-preferred client used by the adapter to talk to
// `open.bigmodel.cn`.
//
// Wire shape (Architect Round 1 OQ-4.4-3 ratification — Story 4.4):
// COMPAT-MODE endpoint
// `https://open.bigmodel.cn/api/paas/v4/chat/completions`.
// Compat-mode body shape and `usage` shape both match OpenAI verbatim
// per Zhipu platform docs. Request translation is IDENTITY-MAPPING
// (BR-1.7 (f)); per-vendor translate.go content cascade resolves to
// identity per OQ-4.4-3.
//
// REUSE NOTE: the wire types below mirror Story-4.1/4.2/4.3 vendor
// `internal/upstream/types.go` bit-for-bit because Zhipu v4 ===
// OpenAI === every Epic-4 vendor's compat-mode shape. Architect Round 1
// L2 ratification: per-vendor `errors.go` + `types.go` are REPLICATED
// (NOT lifted) — `internal/` packages can't be imported across
// `apps/adapters/<vendor>/` module boundaries. Acceptable code
// duplication for organisational clarity.
package upstream

import "encoding/json"

// RawUsage is the upstream JSON shape. Zhipu v4 OpenAI-compat emits
// OpenAI-aligned field names (prompt_tokens / completion_tokens /
// total_tokens); the Normaliser implementation in
// internal/usage/normaliser.go is identity-mapping per OQ-4.4-3 cascade.
type RawUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatMessage is one of the upstream RESPONSE `choices[].message` entries —
// content is always a plain string from the vendor.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// RequestMessage is one OUTBOUND request `messages` entry. Story 9.5 (BR-2.3):
// Content is json.RawMessage so it carries EITHER a plain string (legacy text,
// byte-identical) OR a multipart array [{type,text},{type,image_url,…}] for a
// Vision request (forwarded via the proto ChatMessage.content_parts_json tag 3).
// Request-only so the shared response-decode ChatMessage stays a plain string.
type RequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ChatRequestJSON is the JSON body sent to Zhipu v4 API upstream.
// Identity-mapped from the Connect-RPC ChatRequest per OQ-4.4-3.
type ChatRequestJSON struct {
	Model          string             `json:"model"`
	Messages       []RequestMessage   `json:"messages"`
	Stream         bool               `json:"stream,omitempty"`
	Temperature    *float64           `json:"temperature,omitempty"`
	MaxTokens      *int32             `json:"max_tokens,omitempty"`
	Tools          interface{}        `json:"tools,omitempty"`
	ToolChoice     interface{}        `json:"tool_choice,omitempty"`
	ResponseFormat interface{}        `json:"response_format,omitempty"`
	StreamOptions  *StreamOptionsJSON `json:"stream_options,omitempty"`
}

// StreamOptionsJSON encodes the OpenAI stream_options.include_usage
// convention. BR-2.9: when stream=true the adapter forces
// include_usage=true regardless of what the gateway forwarded.
type StreamOptionsJSON struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatChoiceJSON is one of the upstream response `choices` entries.
type ChatChoiceJSON struct {
	Index        int            `json:"index"`
	Message      *ChatMessage   `json:"message,omitempty"`
	Delta        *ChatDeltaJSON `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

// ChatDeltaJSON is the streaming-chunk content delta.
type ChatDeltaJSON struct {
	Role    *string `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

// ChatResponseJSON is the upstream non-streaming response body.
type ChatResponseJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []ChatChoiceJSON `json:"choices"`
	Usage   *RawUsage        `json:"usage,omitempty"`
}

// ChatChunkJSON is the upstream streaming-chunk shape.
type ChatChunkJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []ChatChoiceJSON `json:"choices"`
	Usage   *RawUsage        `json:"usage,omitempty"`
}
