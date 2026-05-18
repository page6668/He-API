// Story 3.3 — Non-streaming /v1/chat/completions handler (mock upstream).
//
// Replaces the Story-3.2 `chatPlaceholder` 501 stub in cmd/server/main.go.
// Returns a deterministic mock chat.completion response so the OpenAI Python
// SDK can parse the gateway end-to-end before Epic 4 wires the real
// ModelAdapterService client.
//
// BR-4.1 contract caveat: usage numbers are synthetic ({10, 20, 30}); real
// tokenizer-derived counts land in Epic 4 via the adapter's reported usage.
// SDK consumers writing budget logic against this Story's response WILL see
// incorrect numbers — documented as a known-mock-mode caveat.
//
// Error-envelope note (BR-1.3 alignment):
//
//	Story-3.3 BR-1.3 references the auth.go writeJSON / writeError helpers,
//	but those emit a two-field {error:{code,message}} envelope and a
//	Content-Type without `charset=utf-8`. Story-3.2 BR-1.5 ratified the
//	OpenAI §5.1.2 five-field envelope ({error:{code,message,type,param,
//	he_request_id}}) via middleware.writeAPIKeyError. This handler mirrors
//	the §5.1.2 shape verbatim — that envelope IS the contract; the auth.go
//	helpers predate it. No new envelope is being defined.
package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// MockContent is the literal assistant-message content the mock returns.
// Exported so the Story-3.3 unit tests + Epic 4 contract tests can assert
// against a single source of truth (BR-3.3). The string contains the
// substring "He-API mock" (BR-4.2) so log-grep + dashboard filters can
// distinguish leftover mock traffic from real upstream traffic during the
// Epic 4 cutover. ASCII-safe per BR-4.4 (no emoji, no non-ASCII runes —
// removes a second-order Pydantic-strict-mode parsing failure mode).
const MockContent = "Hello from He-API mock. Real upstream lands in Story 4.x."

// maxChatBodyBytes is the BR-1.2 cap (1 MiB). Accommodates ~250k tokens of
// system+user prompt while preventing a single oversized payload from
// monopolising gateway memory. Larger limits (~8 MiB for vision payloads)
// are deferred to Epic 9 when multimodal content lands.
const maxChatBodyBytes int64 = 1 << 20

// messagesMaxLen is the BR-1.4 (AC1 Data Validation) cap on messages length.
const messagesMaxLen = 256

// modelMaxLen mirrors data-models.md §4.1 models.id VARCHAR(100).
const modelMaxLen = 100

// idHexLen is the BR-1.4 entropy floor: 12 lowercase hex chars from
// crypto/rand (48 bits → ~3.6e-12 collision probability over 1000 samples).
const idHexLen = 12

// idPrefix is the BR-1.4 / OQ3 prefix family marker. Epic 4 real adapters
// will emit `chatcmpl-<adapter-suffix>` ids; this Story uses `-mock-` as
// the differentiator so log-grep stays clean during the cutover.
const idPrefix = "chatcmpl-mock-"

// ----- Types (OpenAI-shape) ---------------------------------------------

// ChatRequest is the OpenAI-compatible inbound JSON body. Unknown fields
// are tolerated (passthrough via json.RawMessage for forward-compat with
// Stories 3.4-3.6 and Epic 4 parameter validation).
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Stream         bool            `json:"stream"`
	Temperature    json.RawMessage `json:"temperature,omitempty"`
	MaxTokens      json.RawMessage `json:"max_tokens,omitempty"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

// ChatMessage matches the OpenAI message shape — string content only in
// this Story; multipart content arrays land in Epic 9 (multimodal).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse mirrors rest-api-spec.md §5.1.1 success body. Field order
// matches the OpenAI SDK expectation (id → object → created → model →
// choices → usage); the struct field declaration order drives the JSON
// marshal order via encoding/json.
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
}

// ChatChoice is one of the (currently always single-element) choices array
// entries.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatUsage carries the synthetic mock token counts. Per BR-4.1 the
// numbers are intentional + suspiciously round so SDK consumers can spot
// "this is mock data" by eye.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// validRoles is the BR-2.2 role-set: exactly {system, user, assistant,
// tool}. `developer` (late-2024 GPT-4o) is deferred to Epic 4; `function`
// is the legacy alias for `tool` and is NOT accepted.
var validRoles = map[string]struct{}{
	"system":    {},
	"user":      {},
	"assistant": {},
	"tool":      {},
}

// ----- Constructor option pattern (BR-1.4 / BR-4.5 / OQ4) ---------------

// ChatHandlerOption customizes a ChatCompletionsHandler at construction
// time. The only currently-defined option is WithIDFactory(...).
type ChatHandlerOption func(*ChatCompletionsHandler)

// WithIDFactory replaces the default id factory with f. Tests pass a
// deterministic stub for golden-file assertions; production callers omit
// this option to get crypto/rand-backed ids. Per Architect Round 1 OQ4
// ruling, this option-function pattern replaces a package-mutable
// `var handlers.NewID` (parallel-test-brittle); precedented by
// NewAPIKeyAuthenticator at apps/api-gateway/internal/middleware/bearer_auth.go:204.
func WithIDFactory(f func() string) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if f != nil {
			h.newID = f
		}
	}
}

// WithNow replaces the default time source with f. Story 3.4 AC2 / golden-file
// regression test (3.4-INT-007) need deterministic created timestamps + TTFB
// measurements without clock-sourcing variance. Nil is silently ignored —
// production callers omit this option to get time.Now.
func WithNow(f func() time.Time) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if f != nil {
			h.now = f
		}
	}
}

// ChatCompletionsHandler is the concrete handler. Construct once at startup
// and reuse across all bearer-protected /v1/chat/completions requests.
type ChatCompletionsHandler struct {
	logger *slog.Logger
	newID  func() string // injected via WithIDFactory; production default newMockCompletionID
	now    func() time.Time
}

// NewChatCompletionsHandler builds the handler. logger may be nil — falls
// back to slog.Default(). Production wires no options; tests pass
// WithIDFactory(stub).
func NewChatCompletionsHandler(logger *slog.Logger, opts ...ChatHandlerOption) *ChatCompletionsHandler {
	h := &ChatCompletionsHandler{
		logger: logger,
		newID:  newMockCompletionID,
		now:    time.Now,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// newMockCompletionID is the production id factory (package-private).
// Reads 6 bytes from crypto/rand.Reader, hex-encodes to 12 chars, prefixes
// `chatcmpl-mock-`. Failure to read crypto/rand panics — the same posture
// as the bearer middleware's cache-key derivation.
func newMockCompletionID() string {
	var buf [idHexLen / 2]byte // 6 bytes → 12 hex chars
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand.Reader read failures indicate a broken OS RNG; the
		// Go stdlib treats this as fatal in practice (crypto/tls panics
		// similarly).
		panic("chat_completions: crypto/rand.Reader read failed: " + err.Error())
	}
	return idPrefix + hex.EncodeToString(buf[:])
}

// ServeHTTP implements http.Handler. The bearer-auth middleware (Story
// 3.2) must have already populated the request context with APIKeyID +
// BearerUserID — defence-in-depth check on missing context emits 500
// gateway_misconfigured (BR-1.1; precedent: 2fa_disable.go:38).
func (h *ChatCompletionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// BR-1.1 defence-in-depth — the route registration MUST wrap this
	// handler in middleware.APIKeyAuthenticator.RequireAPIKey. If wiring
	// regresses (e.g., main.go edit drops the wrap), surface a 500 rather
	// than serving anonymous mock content.
	apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured",
			"Bearer-auth middleware not wired", nil)
		return
	}

	// BR-1.2 — MaxBytesReader bounds body reads at 1 MiB. The json decoder
	// surfaces MaxBytesReader truncation as *http.MaxBytesError (Go 1.19+);
	// match it explicitly to return 413 instead of the generic 400.
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			_ = openaierr.Write(w, ctx, http.StatusRequestEntityTooLarge,
				"413_payload_too_large",
				"Request body exceeds 1 MiB.", nil)
			return
		}
		_ = openaierr.Write(w, ctx, http.StatusBadRequest,
			"400_invalid_request",
			"Request body is not valid JSON.", nil)
		return
	}

	// BR-2.1 ordered validation (steps 3-5). Body-size + JSON parse are
	// handled above (steps 1-2). validateChatRequest stops on the first
	// failing rule. Per Story 3.4 T3.4, the stream=true → 501 branch is
	// REMOVED; stream=true is now a dispatch decision, not a validation
	// failure.
	if status, code, msg, valid := validateChatRequest(&req); !valid {
		_ = openaierr.Write(w, ctx, status, code, msg, nil)
		return
	}

	// Story 3.4 BR-1.1 dispatch fork — stream=true requests serve SSE; the
	// non-streaming path below is preserved byte-for-byte for stream=false
	// (and stream omitted, which defaults to false via Go's bool zero-value).
	if req.Stream {
		h.serveStream(w, r, &req, apiKeyID)
		return
	}

	// BR-1.8 structured log — emitted exactly once per accepted request.
	// PII (messages[].content) is intentionally NOT logged.
	h.logger.InfoContext(
		ctx, "chat_completions_mock",
		slog.String("event", "chat_completions_mock"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
	)

	resp := mockChatCompletionResponse(&req, h.newID(), h.now())
	writeChatJSON(w, http.StatusOK, resp)
}

// mockChatCompletionResponse builds the deterministic mock body. Per
// BR-4.3 the response depends only on req.Model (echoed) — same input
// always produces the same output modulo id randomness + created
// timestamp. No "echo last user message", no "vary by temperature".
func mockChatCompletionResponse(req *ChatRequest, id string, now time.Time) *ChatResponse {
	return &ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: now.UTC().Unix(),
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: MockContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}
}

// validateChatRequest applies BR-2.1 ordered validation rules. Returns
// (0, "", "", true) on success and (status, code, message, false) on the
// first failing rule. Pure function — no I/O, no allocations beyond the
// result strings.
func validateChatRequest(req *ChatRequest) (int, string, string, bool) {
	if req.Model == "" || len(req.Model) > modelMaxLen {
		return http.StatusBadRequest,
			"400_invalid_request",
			"Field 'model' is required and must be a non-empty string.",
			false
	}
	if len(req.Messages) == 0 || len(req.Messages) > messagesMaxLen {
		return http.StatusBadRequest,
			"400_invalid_request",
			"Field 'messages' must be a non-empty array (max 256 entries) with each entry having role + string content.",
			false
	}
	for i := range req.Messages {
		m := &req.Messages[i]
		if _, ok := validRoles[m.Role]; !ok || m.Content == "" {
			return http.StatusBadRequest,
				"400_invalid_request",
				"Field 'messages' must be a non-empty array (max 256 entries) with each entry having role + string content.",
				false
		}
	}
	return 0, "", "", true
}

// ----- Local envelope writers (§5.1.2-aligned) --------------------------
//
// writeChatJSON / writeChatError emit `application/json; charset=utf-8`
// and (for errors) the §5.1.2 5-field envelope. See file-header note on
// why these diverge from auth.go's writeJSON / writeError helpers.

func writeChatJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// json.Marshal (not json.Encoder) so the body has no trailing newline.
	buf, _ := json.Marshal(body)
	_, _ = w.Write(buf)
}

// Story 3.6: writeChatError + mapChatErrorType + paramValue were deleted in
// favour of openaierr.Write — the canonical §5.1.2 writer that derives
// error.type from codeMetadata and stamps he_request_id from
// requestid.FromContext. The paramValue semantics are preserved verbatim
// inside openaierr.
