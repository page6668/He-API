// Package upstream owns the Baidu Qianfan v2 HTTPS-API wire types and the
// HTTP/2-preferred client used by the adapter to talk to
// `qianfan.baidubce.com`.
//
// Wire shape (Architect Round 1 OQ-4.6-3 ratification — Story 4.6):
// QIANFAN V2 OpenAI-compat endpoint
// `https://qianfan.baidubce.com/v2/chat/completions`.
// Compat-mode body shape and `usage` shape both match OpenAI verbatim
// per Baidu Qianfan v2 platform docs. Request translation is IDENTITY-
// MAPPING (BR-1.7 (f)); per-vendor translate.go content cascade resolves
// to identity per OQ-4.6-3.
//
// REUSE NOTE: the wire types below mirror Story-4.1/4.2/4.3/4.4/4.5
// vendor `internal/upstream/types.go` bit-for-bit because Qianfan v2 ===
// OpenAI === every Epic-4 vendor's compat-mode shape. Architect Round 1
// L2 ratification: per-vendor `errors.go` + `types.go` are REPLICATED
// (NOT lifted) — `internal/` packages can't be imported across
// `apps/adapters/<vendor>/` module boundaries. Acceptable code
// duplication for organisational clarity.
package upstream

// RawUsage is the upstream JSON shape. Qianfan v2 OpenAI-compat emits
// OpenAI-aligned field names (prompt_tokens / completion_tokens /
// total_tokens); the Normaliser implementation in
// internal/usage/normaliser.go is identity-mapping per OQ-4.6-3 cascade.
type RawUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatMessage is one of the request `messages` entries.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequestJSON is the JSON body sent to Qianfan v2 API upstream.
// Identity-mapped from the Connect-RPC ChatRequest per OQ-4.6-3.
type ChatRequestJSON struct {
	Model          string             `json:"model"`
	Messages       []ChatMessage      `json:"messages"`
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
