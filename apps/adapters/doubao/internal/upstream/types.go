package upstream

// Wire shape (Architect Round 1 OQ-4.5-1 + OQ-4.5-3 ratifications, Story 4.5):
// Volcengine Ark v3 OpenAI-compat endpoint
// `https://ark.cn-beijing.volces.com/api/v3/chat/completions`. Compat-mode
// body / response / `usage` shapes match OpenAI verbatim per Volcengine Ark
// platform docs.
//
// The ONLY non-identity transform in Story 4.5 is the `model` field
// bidirectional rewrite (friendly id ↔ Volcengine endpoint id) per BR-1.11
// + BR-1.7.f — implemented in translate.go. All other fields are identity-
// mapped per OQ-4.5-3 cascade from Story-4.2 OQ-4.2-3.
//
// REUSE NOTE: the wire types below mirror Story-4.4 glm `internal/upstream/types.go`
// bit-for-bit because Volcengine Ark v3 OpenAI-compat shape = OpenAI shape =
// every Epic-4 vendor's compat-mode shape. Per Architect Round 1 L2
// ratification: per-vendor `errors.go` + `types.go` are REPLICATED (NOT
// lifted) — `internal/` packages can't be imported across
// `apps/adapters/<vendor>/` module boundaries. Acceptable duplication for
// organisational clarity.

// RawUsage is the upstream JSON shape. Volcengine Ark v3 OpenAI-compat
// emits OpenAI-aligned field names; the Normaliser in
// internal/usage/normaliser.go is identity-mapping per OQ-4.5-3 cascade.
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

// ChatRequestJSON is the JSON body sent to Volcengine Ark v3 upstream.
// Identity-mapped from the Connect-RPC ChatRequest per OQ-4.5-3 EXCEPT
// for the `model` field which is REWRITTEN from the friendly id to the
// Volcengine endpoint id by translate.go per BR-1.7.f.
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

// ChatResponseJSON is the upstream non-streaming response body. Volcengine
// echoes the endpoint id in `Model` — translate.go's TranslateChatResponse
// REWRITES it back to the friendly id per BR-1.11 inbound back-translate
// before returning to the gateway.
type ChatResponseJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []ChatChoiceJSON `json:"choices"`
	Usage   *RawUsage        `json:"usage,omitempty"`
}

// ChatChunkJSON is the upstream streaming-chunk shape. Same Volcengine
// `Model = endpoint id` echo convention — TranslateChatChunk per-chunk
// REWRITES it back to the friendly id per BR-1.11.
type ChatChunkJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []ChatChoiceJSON `json:"choices"`
	Usage   *RawUsage        `json:"usage,omitempty"`
}
