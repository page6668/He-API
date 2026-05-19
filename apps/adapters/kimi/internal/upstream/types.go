// Package upstream owns the Kimi Moonshot HTTPS-API wire types and the
// HTTP/2-preferred client used by the adapter to talk to
// `api.moonshot.cn`.
//
// Wire shape (Architect Round 1 OQ-4.2-1 ratification — Story 4.2):
// COMPAT-MODE endpoint
// `https://api.moonshot.cn/v1/chat/completions`.
// Compat-mode body shape and `usage` shape both match OpenAI verbatim.
// Request translation is IDENTITY-MAPPING (BR-1.7 (f)); per-vendor
// translate.go content cascade resolves to identity per OQ-4.2-3.
//
// REUSE NOTE: the wire types below mirror Story-4.1 deepseek/internal/
// upstream/types.go bit-for-bit because Moonshot === OpenAI === DeepSeek's
// own shape. Architect Round 1 L2 ratification: per-vendor `errors.go` +
// `types.go` are REPLICATED (NOT lifted) — `internal/` packages can't be
// imported across `apps/adapters/<vendor>/` module boundaries. Acceptable
// code duplication for organisational clarity.
package upstream

// RawUsage is the upstream JSON shape. Kimi Moonshot emits OpenAI-
// aligned field names (prompt_tokens / completion_tokens / total_tokens);
// IF a future Kimi-native endpoint is added, the Normaliser implementation
// in internal/usage/normaliser.go performs the input_tokens → prompt_tokens
// rename. For Moonshot the Normaliser is identity-mapping.
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

// ChatRequestJSON is the JSON body sent to Moonshot API upstream.
// Identity-mapped from the Connect-RPC ChatRequest per OQ-4.2-1.
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
