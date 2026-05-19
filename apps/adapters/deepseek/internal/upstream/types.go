// Package upstream owns the DeepSeek HTTPS-API wire types and the
// HTTP/2-forced client used by the adapter to talk to api.deepseek.com.
//
// Wire shape: DeepSeek's `/v1/chat/completions` endpoint is OpenAI-protocol-
// native — request and response JSON match the OpenAI shapes verbatim. For
// Story 4.1 the request translation is identity-mapping (BR-1.7 (f));
// Stories 4.2-4.6 supply non-identity translations for vendors whose request
// body shapes differ.
package upstream

// RawUsage is the upstream JSON shape. DeepSeek returns OpenAI-canonical
// field names (BR-3.2); other vendors will need a Normaliser implementation
// (Stories 4.2-4.6).
type RawUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatMessage is one of the request `messages` entries (role + string content).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequestJSON is the JSON body sent to DeepSeek upstream. Identity-
// mapped from the Connect-RPC ChatRequest for Story 4.1.
type ChatRequestJSON struct {
	Model         string             `json:"model"`
	Messages      []ChatMessage      `json:"messages"`
	Stream        bool               `json:"stream,omitempty"`
	Temperature   *float64           `json:"temperature,omitempty"`
	MaxTokens     *int32             `json:"max_tokens,omitempty"`
	Tools         interface{}        `json:"tools,omitempty"`
	ToolChoice    interface{}        `json:"tool_choice,omitempty"`
	ResponseFormat interface{}       `json:"response_format,omitempty"`
	StreamOptions *StreamOptionsJSON `json:"stream_options,omitempty"`
}

// StreamOptionsJSON encodes OpenAI's stream_options.include_usage convention.
// BR-2.9: when stream=true the adapter forces include_usage=true regardless
// of what the gateway forwarded (so the tail-usage chunk is always present).
type StreamOptionsJSON struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatChoiceJSON is one of the upstream response `choices` entries on the
// non-streaming path (and the equivalent chunk shape on the streaming path
// — DeepSeek's SSE frames carry the same structure with `delta` instead of
// `message`).
type ChatChoiceJSON struct {
	Index        int             `json:"index"`
	Message      *ChatMessage    `json:"message,omitempty"`
	Delta        *ChatDeltaJSON  `json:"delta,omitempty"`
	FinishReason *string         `json:"finish_reason"`
}

// ChatDeltaJSON is the streaming-chunk content delta. Role + content are
// both optional — the first chunk typically carries `role` only; subsequent
// chunks carry `content` only; the terminal chunk carries neither.
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

// ChatChunkJSON is the upstream streaming-chunk shape (per SSE `data: <json>`
// frame). Differs from ChatResponseJSON only in the per-choice Delta-vs-
// Message field selection.
type ChatChunkJSON struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []ChatChoiceJSON `json:"choices"`
	Usage   *RawUsage        `json:"usage,omitempty"`
}
