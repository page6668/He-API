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
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
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

// WithAdapterRegistry wires the model-id → adapter-endpoint resolver. When
// a request's req.Model resolves via the registry (Story 4.1 — only
// "deepseek-v3"), the handler dispatches to the real adapter Connect-RPC
// instead of writing the Story-3.3 mock response. Models that miss the
// registry continue to receive the mock (until Stories 4.2-4.6 register
// their entries; Story 4.7 cuts over the capability matrix and retires
// the mock-fallback path).
//
// Nil is silently ignored — production wires a non-nil registry from
// adapterclient.LoadFromEnv() at startup; tests may pass nil to exercise
// the pure mock path.
func WithAdapterRegistry(reg *adapterclient.Registry) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if reg != nil {
			h.adapterRegistry = reg
		}
	}
}

// WithRouter wires the Story-6.2 routing Decider. When set, the handler
// consults routing-svc on every chat request to derive the selected model
// (resolving he-router-* meta-models + the X-He-Routing-Strategy header) and
// surfaces it on X-He-Selected-Model (BR1-2).
//
// Nil is silently ignored — the constructor installs a passthrough Decider
// (nil client) by default, so a handler built without this option behaves
// exactly as it did pre-6.2: selected == req.Model on every path.
func WithRouter(d *routingclient.Decider) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if d != nil {
			h.router = d
		}
	}
}

// WithTokenDeducter wires the Story-5.3 TPM post-deduction hook. The
// handler invokes deducter.TPMDeduct(ctx, apiKeyID, usage.total_tokens)
// after the upstream returns on the non-streaming success paths (mock
// + adapter non-stream). Streaming TPM deduction lands on the adapter-
// chunker tail-usage chunk in a Phase-2 follow-up (BR-3.5 wiring).
//
// Nil is silently treated as a no-op deducter — production wires a
// non-nil *ratelimit.Middleware at startup; tests may omit.
func WithTokenDeducter(d TokenDeducter) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if d != nil {
			h.tokenDeducter = d
		}
	}
}

// WithFailoverBudget overrides the Story-6.3 total wall-clock failover budget
// (default FailoverBudget=30s). It exists so the budget-exhaustion behaviour
// (Q-B/Q-G, 6.3-UNIT-018) is testable deterministically with a small budget +
// a blocking upstream, instead of a 30s real-time wait. Production never wires
// it (the 30s const governs). A non-positive value is ignored.
func WithFailoverBudget(d time.Duration) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if d > 0 {
			h.failoverBudget = d
		}
	}
}

// ChatCompletionsHandler is the concrete handler. Construct once at startup
// and reuse across all bearer-protected /v1/chat/completions requests.
type ChatCompletionsHandler struct {
	logger          *slog.Logger
	newID           func() string // injected via WithIDFactory; production default newMockCompletionID
	now             func() time.Time
	adapterRegistry *adapterclient.Registry
	tokenDeducter   TokenDeducter          // Story 5.3 — post-response TPM deduction; nil → nop
	router          *routingclient.Decider // Story 6.2 — routing decision; default passthrough
	failoverBudget  time.Duration          // Story 6.3 — total wall-clock failover budget (default FailoverBudget)
}

// NewChatCompletionsHandler builds the handler. logger may be nil — falls
// back to slog.Default(). Production wires no options; tests pass
// WithIDFactory(stub).
func NewChatCompletionsHandler(logger *slog.Logger, opts ...ChatHandlerOption) *ChatCompletionsHandler {
	h := &ChatCompletionsHandler{
		logger:         logger,
		newID:          newMockCompletionID,
		now:            time.Now,
		tokenDeducter:  nopTokenDeducter{},
		failoverBudget: FailoverBudget, // Story 6.3 default; WithFailoverBudget overrides for tests
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	// Story 6.2 — default to a passthrough Decider (nil client): selected ==
	// req.Model on every path until WithRouter wires a real routing-svc client.
	h.router = routingclient.NewDecider(nil, h.logger)
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

	// Story 6.2 AC1 — routing decision. The gateway consults routing-svc to
	// derive the selected model (resolving he-router-* meta-models + the
	// X-He-Routing-Strategy header, Q-I) BEFORE the stream/adapter fork; the
	// returned `selected` replaces req.Model for adapter resolution, the
	// X-He-Selected-Model header, and the response model echo. A fail-closed /
	// invalid outcome (Q-G/Q-H) writes the §5.1.2 envelope and returns.
	selected, failoverChain, strategyLabel, ok := h.routeOrWriteError(w, r, &req, apiKeyID)
	if !ok {
		return
	}

	// Story 3.4 BR-1.1 dispatch fork — stream=true requests serve SSE; the
	// non-streaming path below is preserved byte-for-byte for stream=false
	// (and stream omitted, which defaults to false via Go's bool zero-value).
	if req.Stream {
		h.serveStream(w, r, &req, apiKeyID, selected, failoverChain, strategyLabel)
		return
	}

	// Story 4.1 BR-1.2 adapter-dispatch fork — BEFORE the existing mock-
	// write block, check the model-id resolver against the ROUTED model
	// (Story 6.2 — was req.Model). Hit → real adapter; miss → fall through to
	// the Story-3.3 mock path. Story 6.3 m-1: the Resolve→dispatch unit is now
	// wrapped in the failover loop (dispatchNonStreamWithFailover), which
	// re-resolves per hop; this Resolve only decides the adapter-vs-mock fork on
	// the PRIMARY model (the mock path has no failover).
	if h.adapterRegistry != nil {
		if _, ok := h.adapterRegistry.Resolve(selected); ok {
			h.dispatchNonStreamWithFailover(w, r, &req, apiKeyID, selected, failoverChain, strategyLabel)
			return
		}
	}

	// BR-1.8 structured log — emitted exactly once per accepted request.
	// PII (messages[].content) is intentionally NOT logged.
	h.logger.InfoContext(
		ctx, "chat_completions_mock",
		slog.String("event", "chat_completions_mock"),
		slog.String("model", req.Model),
		slog.String("selected_model", selected),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
	)

	// BR1-2 — X-He-Selected-Model is a universal success-path invariant (set on
	// the mock path too; Story 6.2 High-2 / UNIT-013 flip).
	w.Header().Set("X-He-Selected-Model", selected)
	resp := mockChatCompletionResponse(&req, selected, h.newID(), h.now())
	writeChatJSON(w, http.StatusOK, resp)
	// Story 5.3 BR-3.4 / Architect Q3 — post-deduction on success. Fire-
	// and-forget; errors are absorbed by the deducter (slog WARN inside
	// the ratelimit package).
	h.tokenDeducter.TPMDeduct(ctx, apiKeyID, resp.Usage.TotalTokens)
}

// routeOrWriteError runs the Story-6.2 routing decision and returns the model
// the request should dispatch to. On a fail-closed / invalid decision (Q-G/Q-H)
// it writes the canonical §5.1.2 envelope (BR4-3) and returns ok=false. The
// 100ms deadline (Q-E) is applied inside the Decider. The decision is echoed to
// slog (non-PII) here so every dispatch path shares one decision log.
func (h *ChatCompletionsHandler) routeOrWriteError(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID string) (selected string, failoverChain []string, strategy string, ok bool) {
	ctx := r.Context()
	heRequestID, _ := requestid.FromContext(ctx)
	userID, _ := middleware.BearerUserIDFromContext(ctx)

	decision, err := h.router.Decide(ctx, req.Model, r.Header, userID, heRequestID)
	if err != nil {
		var ee *routingclient.EnvelopeError
		if errors.As(err, &ee) {
			_ = openaierr.Write(w, ctx, 0, ee.Code, routingEnvelopeMessage(ee.Code), nil)
			return "", nil, "", false
		}
		// Defensive — an unexpected non-envelope error degrades to 502.
		_ = openaierr.Write(w, ctx, 0, "502_upstream_unavailable",
			"Upstream model service is unavailable, please retry.", nil)
		return "", nil, "", false
	}

	// Story 6.3 Q-K / BR2-5 — scope-filter the failover tail BEFORE dispatch so
	// failover NEVER targets a model the key is not authorised for. The key's
	// scope.models comes from the Story-5.2 keypolicy cache claims; an empty
	// scope means "all models allowed". selected_model (chain[0]) already passed
	// the gate via 6.2, so it is dispatched as-is.
	var scopeModels []string
	if claims, hasClaims := middleware.CacheValueFromContext(ctx); hasClaims {
		scopeModels = claims.ScopeModels
	}
	failoverChain = routingclient.FilterByScope(decision.FailoverChain, scopeModels)

	h.logger.LogAttrs(ctx, slog.LevelInfo, "chat_completions_routing_decision",
		slog.String("event", "chat_completions_routing_decision"),
		slog.String("requested_model", req.Model),
		slog.String("selected_model", decision.SelectedModel),
		slog.String("strategy", decision.Strategy.String()),
		slog.String("score_source", decision.ScoreSource),
		slog.Bool("bypassed", decision.Bypassed),
		slog.Int("failover_chain_len", len(failoverChain)), // Story 6.3 (non-PII)
		slog.String("he_request_id", heRequestID),
	) // BR4-2 non-PII: NEVER user_id / api_key content.
	return decision.SelectedModel, failoverChain, decision.Strategy.String(), true
}

// routingEnvelopeMessage returns the user-facing message for a routing envelope
// code (Q-H). Reuses the wording from the adapter-error mapping (§5.1.2).
func routingEnvelopeMessage(code string) string {
	switch code {
	case "400_invalid_request":
		return "The requested model is not available."
	case "502_upstream_unavailable":
		return "No upstream model is available for the requested routing strategy."
	default:
		return "Upstream model service is unavailable, please retry."
	}
}

// serveAdapterNonStream performs ONE upstream attempt against handle (Story 6.3
// m-1 — the single-attempt dispatch primitive invoked per failover hop). On
// success it writes the OpenAI chat.completion body + X-He-Selected-Model
// (= servedModel, Q-F) + TPMDeduct (BR2-4 exactly-once) and returns nil. On an
// upstream fault it returns the error WITHOUT writing anything to w, so the
// caller (the failover loop) can classify + advance (BR2-1). It NEVER writes an
// error envelope itself — terminal envelope writing is the loop's job.
//
// ctx is the failover-budgeted context (Q-G); servedModel is the model this hop
// dispatches to (== the model echoed in the body + header on success).
func (h *ChatCompletionsHandler) serveAdapterNonStream(ctx context.Context, w http.ResponseWriter, req *ChatRequest, apiKeyID, servedModel string, handle adapterclient.ClientHandle) error {
	heRequestID, _ := requestid.FromContext(ctx)

	adapterReq := buildAdapterRequest(req, servedModel, heRequestID)
	headers := http.Header{}
	if heRequestID != "" {
		// BR-1.5 — propagate to the adapter Connect-RPC as a header AND
		// inside the proto field (defence-in-depth per m2; redundancy is
		// intentional for Story 4.1, may be tightened in Story 4.7).
		headers.Set("X-He-Request-Id", heRequestID)
	}

	stream, err := handle.Chat(ctx, adapterReq, headers)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	if !stream.Receive() {
		// No chunk emitted — treat as upstream invalid response per BR-1.4.
		if e := stream.Err(); e != nil {
			return e
		}
		return errUpstreamInvalidResponse
	}
	chunk := stream.Msg()
	// Drain any subsequent chunks defensively — non-streaming path expects
	// exactly one chunk. Stream.Err() surfaces mid-iteration errors.
	for stream.Receive() {
		// ignore extras
	}
	if e := stream.Err(); e != nil {
		return e
	}
	if chunk == nil || chunk.Usage == nil || len(chunk.Choices) == 0 {
		return errUpstreamInvalidResponse
	}

	resp := adapterChunkToResponse(chunk, servedModel, h.now())

	// BR1-2 / Q-F — gateway sets X-He-Selected-Model = the model that actually
	// SERVED (after any failover hops; == the body `model` echo).
	w.Header().Set("X-He-Selected-Model", servedModel)
	writeChatJSON(w, http.StatusOK, resp)

	// Story 5.3 BR-3.4 / Architect Q3 — post-deduction on success. BR2-4: this
	// is the ONLY TPMDeduct per request; failed attempts carry no usage and
	// never reach here.
	h.tokenDeducter.TPMDeduct(ctx, apiKeyID, int(chunk.Usage.GetTotalTokens()))

	h.logger.InfoContext(
		ctx, "chat_completions_adapter",
		slog.String("event", "chat_completions_adapter"),
		slog.String("model", req.Model),
		slog.String("served_model", servedModel), // Story 6.3 Q-F (non-PII)
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("prompt_tokens", int(chunk.Usage.GetPromptTokens())),
		slog.Int("completion_tokens", int(chunk.Usage.GetCompletionTokens())),
		slog.Int("total_tokens", int(chunk.Usage.GetTotalTokens())),
	)
	return nil
}

// dispatchNonStreamWithFailover is the Story-6.3 non-streaming failover loop
// (AC2). It iterates the scope-filtered candidate chain (selected_model +
// failoverChain), dispatching each resolvable hop via serveAdapterNonStream,
// advancing on a retriable upstream fault (502/504 — Q-C) until success, the
// MaxFailoverAttempts cap, the FailoverBudget wall-clock, or chain exhaustion.
// On the first success the body is already written by the primitive; on
// exhaustion / a non-retriable outcome it writes the terminal §5.1.2 envelope
// ONCE (BR2-1). The happy path (first attempt succeeds) is byte-for-byte the
// 6.2 path plus a single 1-attempt histogram observation (BR4-2).
func (h *ChatCompletionsHandler) dispatchNonStreamWithFailover(w http.ResponseWriter, r *http.Request, req *ChatRequest, apiKeyID, selected string, failoverChain []string, strategy string) {
	// Q-G — a parent budget wraps the whole loop; each attempt keeps its own
	// per-adapter deadline. defer cancel on EVERY exit path (BLIND-RESOURCE-002).
	ctx, cancel := context.WithTimeout(r.Context(), h.failoverBudget)
	defer cancel()

	chain := append([]string{selected}, failoverChain...)

	attempts := 0
	var lastErr error
	var prevModel, prevReason string

	for _, model := range chain {
		if ctx.Err() != nil {
			break // 30s budget exhausted before this hop (Q-B/Q-G)
		}
		handle, ok := h.adapterRegistry.Resolve(model)
		if !ok {
			// BR2-3 — an unresolved chain entry is skipped (logged), NOT counted
			// against the attempt cap (no upstream call was made).
			h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_failover_skip_unresolved",
				slog.String("event", "chat_completions_failover_skip_unresolved"),
				slog.String("model", model),
			)
			continue
		}
		if prevModel != "" {
			// We advanced from a failed hop to this one — record the failover hop
			// (Q-I metric + slog) with the model we are about to try as `to`.
			h.recordFailover(ctx, prevModel, model, prevReason, strategy, attempts+1)
		}
		attempts++

		err := h.serveAdapterNonStream(ctx, w, req, apiKeyID, model, handle)
		if err == nil {
			h.router.RecordFailoverAttempts(ctx, attempts) // BR4-2 — 1 on happy path
			if attempts > 1 {
				h.logFailoverServed(ctx, chain[0], model, attempts) // 6.3-UNIT-048
			}
			return
		}

		code, retriable := isRetriableUpstream(err)
		if !retriable {
			// Q-C — a non-retriable outcome is TERMINAL immediately; no failover.
			h.router.RecordFailoverAttempts(ctx, attempts)
			h.writeFailoverTerminal(w, ctx, req, apiKeyID, err)
			return
		}
		lastErr = err
		prevModel, prevReason = model, failoverReason(code)
		if attempts >= MaxFailoverAttempts {
			break // 3-attempt cap (Q-B/BR2-2)
		}
	}

	// Exhausted — chain end, attempt cap, or budget. Write the LAST upstream
	// error envelope (today's terminal behaviour — zero regression).
	h.router.RecordFailoverAttempts(ctx, attempts)
	if lastErr == nil {
		// Defensive: no resolvable hop made an upstream call (every entry was
		// unresolved, or an empty chain) — surface an upstream-unavailable 502.
		lastErr = errUpstreamInvalidResponse
	}
	h.writeFailoverTerminal(w, ctx, req, apiKeyID, lastErr)
}

// MaxFailoverAttempts caps the TOTAL upstream attempts per request (primary +
// ≤2 failover hops) — the "3 次" half of the title (Q-B / BR2-2).
const MaxFailoverAttempts = 3

// FailoverBudget is the TOTAL wall-clock budget across all attempts — the "30s"
// half (Q-B/Q-G); a parent context.WithTimeout wraps the attempt loop.
const FailoverBudget = 30 * time.Second

// errUpstreamInvalidResponse marks an upstream reply with no usable chunk/usage.
// It is a retriable upstream fault (failover advances) and renders the §5.1.2
// "invalid response" 502 envelope on exhaustion (byte-identical to the pre-6.3
// inline write — no test asserts the message, but the wording is preserved).
var errUpstreamInvalidResponse = errors.New("upstream model service returned an invalid response")

// isRetriableUpstream classifies an adapter dispatch error for the failover loop
// WITHOUT writing (BR2-1 / 6.3-UNIT-028). It returns the §5.1.2 code and whether
// the loop should advance. ONLY the two documented upstream-fault codes are
// retriable (Q-C): 502_upstream_unavailable + 504_upstream_timeout. Epic-4
// collapses upstream 4xx/5xx → Unavailable/Internal, so in production every
// adapter error is retriable; the non-retriable branch (a client/policy connect
// code surfacing as an adapter error) is defensive — it terminates instead of
// fanning a request/policy error across the catalogue.
func isRetriableUpstream(err error) (code string, retriable bool) {
	_, code, _, retriable = classifyFailoverError(err)
	return code, retriable
}

// classifyFailoverError is the single source of truth for the failover path's
// error→envelope mapping (status, §5.1.2 code, message, retriable). For the
// 502/504 cases it is byte-identical to classifyAdapterError (zero regression on
// the concrete-default exhaustion path — 6.3-UNIT-023).
func classifyFailoverError(err error) (status int, code, message string, retriable bool) {
	const (
		msgUnavailable     = "Upstream model service is unavailable, please retry."
		msgTimeout         = "Upstream model service did not respond within the deadline."
		msgInvalidResponse = "Upstream model service returned an invalid response."
		msgInvalidRequest  = "The request was rejected by the upstream model service."
		msgModelNotInScope = "The selected model is not available for this key."
		msgRateLimited     = "The upstream model service is rate limiting the request."
		msgContentFilter   = "The request was blocked by the upstream content filter."
	)
	switch {
	case err == nil:
		return http.StatusBadGateway, "502_upstream_unavailable", msgUnavailable, true
	case errors.Is(err, errUpstreamInvalidResponse):
		return http.StatusBadGateway, "502_upstream_unavailable", msgInvalidResponse, true
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "504_upstream_timeout", msgTimeout, true
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Code() {
		case connect.CodeDeadlineExceeded:
			return http.StatusGatewayTimeout, "504_upstream_timeout", msgTimeout, true
		case connect.CodeUnavailable, connect.CodeInternal, connect.CodeUnknown:
			return http.StatusBadGateway, "502_upstream_unavailable", msgUnavailable, true
		case connect.CodeInvalidArgument:
			return http.StatusBadRequest, "400_invalid_request", msgInvalidRequest, false
		case connect.CodePermissionDenied:
			return http.StatusForbidden, "403_model_not_in_scope", msgModelNotInScope, false
		case connect.CodeResourceExhausted:
			return http.StatusTooManyRequests, "429_rate_limit_qps", msgRateLimited, false
		case connect.CodeFailedPrecondition:
			return http.StatusBadRequest, "400_content_filter", msgContentFilter, false
		default:
			// Q-C note — an upstream misconfiguration is indistinguishable from
			// upstream-down and DOES failover (collapses to a retriable 502).
			return http.StatusBadGateway, "502_upstream_unavailable", msgUnavailable, true
		}
	}
	// Non-Connect (dial / TCP) error — upstream unreachable, retriable.
	return http.StatusBadGateway, "502_upstream_unavailable", msgUnavailable, true
}

// failoverReason maps a §5.1.2 code to the Q-I metric/slog reason label.
func failoverReason(code string) string {
	if code == "504_upstream_timeout" {
		return "upstream_timeout"
	}
	return "upstream_unavailable"
}

// writeFailoverTerminal writes the single terminal §5.1.2 envelope at the end of
// the failover loop (BR2-1 — openaierr.Write stays the sole writer). For 502/504
// it is byte-identical to the pre-6.3 writeAdapterError envelope.
func (h *ChatCompletionsHandler) writeFailoverTerminal(w http.ResponseWriter, ctx context.Context, req *ChatRequest, apiKeyID string, lastErr error) {
	status, code, message, _ := classifyFailoverError(lastErr)
	h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_adapter_error",
		slog.String("event", "chat_completions_adapter_error"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.String("error_code", code),
		slog.String("error", adapterErrString(lastErr)),
	)
	_ = openaierr.Write(w, ctx, status, code, message, nil)
}

// recordFailover emits the Story-6.3 failover hop instrument + a non-PII slog
// line (Q-I / BR4-1). It fires once per advance (from a failed hop to the next
// attempted model). NEVER logs user_id / message content.
func (h *ChatCompletionsHandler) recordFailover(ctx context.Context, fromModel, toModel, reason, strategy string, attempt int) {
	h.router.RecordFailover(ctx, fromModel, toModel, reason)
	heRequestID, _ := requestid.FromContext(ctx)
	h.logger.LogAttrs(ctx, slog.LevelInfo, "routing_failover",
		slog.String("event", "routing_failover"),
		slog.String("routing_action", "failover"), // observability §11.3 label
		slog.String("from_model", fromModel),
		slog.String("to_model", toModel),
		slog.String("reason", reason),
		slog.String("strategy", strategy),
		slog.Int("attempt", attempt),
		slog.String("he_request_id", heRequestID),
	)
}

// logFailoverServed records (on a request that DID fail over) both the originally
// requested model (chain[0]) and the model that actually served (Q-F / 6.3-
// UNIT-048), distinguishing intended-vs-served. Not emitted on the happy path
// (BR4-2 zero-regression).
func (h *ChatCompletionsHandler) logFailoverServed(ctx context.Context, requestedSelected, servedModel string, attempts int) {
	heRequestID, _ := requestid.FromContext(ctx)
	h.logger.LogAttrs(ctx, slog.LevelInfo, "routing_failover_served",
		slog.String("event", "routing_failover_served"),
		slog.String("requested_selected", requestedSelected),
		slog.String("served_model", servedModel),
		slog.Int("failover_count", attempts-1),
		slog.String("he_request_id", heRequestID),
	)
}

// buildAdapterRequest translates the OpenAI-shape ChatRequest into the
// proto-shape adapterv1.ChatRequest. JSON passthrough fields are forwarded
// verbatim (BR-1.7 (f) identity-mapping at the gateway boundary).
func buildAdapterRequest(req *ChatRequest, model, heRequestID string) *adapterv1.ChatRequest {
	messages := make([]*adapterv1.ChatMessage, len(req.Messages))
	for i := range req.Messages {
		messages[i] = &adapterv1.ChatMessage{
			Role:    req.Messages[i].Role,
			Content: req.Messages[i].Content,
		}
	}
	adapterReq := &adapterv1.ChatRequest{
		Model:       model, // Story 6.2 — the ROUTED model (== req.Model on passthrough)
		Messages:    messages,
		Stream:      false, // non-streaming branch
		HeRequestId: heRequestID,
	}
	if len(req.Temperature) > 0 {
		var v float64
		if err := json.Unmarshal(req.Temperature, &v); err == nil {
			adapterReq.Temperature = &v
		}
	}
	if len(req.MaxTokens) > 0 {
		var v int32
		if err := json.Unmarshal(req.MaxTokens, &v); err == nil {
			adapterReq.MaxTokens = &v
		}
	}
	if len(req.Tools) > 0 {
		adapterReq.ToolsJson = []byte(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		adapterReq.ToolChoiceJson = []byte(req.ToolChoice)
	}
	if len(req.ResponseFormat) > 0 {
		adapterReq.ResponseFormatJson = []byte(req.ResponseFormat)
	}
	return adapterReq
}

// adapterChunkToResponse converts the terminal ChatChunk emitted by the
// adapter into the OpenAI chat.completion response body the SDK expects.
func adapterChunkToResponse(chunk *adapterv1.ChatChunk, model string, now time.Time) *ChatResponse {
	choices := make([]ChatChoice, len(chunk.Choices))
	for i, c := range chunk.Choices {
		var (
			role    string
			content string
		)
		if c.Delta != nil {
			if c.Delta.Role != nil {
				role = *c.Delta.Role
			}
			if c.Delta.Content != nil {
				content = *c.Delta.Content
			}
		}
		if role == "" {
			role = "assistant"
		}
		finish := ""
		if c.FinishReason != nil {
			finish = *c.FinishReason
		} else if chunk.FinishReason != nil {
			finish = *chunk.FinishReason
		}
		choices[i] = ChatChoice{
			Index:        int(c.Index),
			Message:      ChatMessage{Role: role, Content: content},
			FinishReason: finish,
		}
	}
	id := chunk.Id
	if id == "" {
		id = "chatcmpl-" + hex.EncodeToString([]byte{0, 0, 0, 0, 0, 0})
	}
	created := chunk.Created
	if created == 0 {
		created = now.UTC().Unix()
	}
	return &ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: choices,
		Usage: ChatUsage{
			PromptTokens:     int(chunk.Usage.GetPromptTokens()),
			CompletionTokens: int(chunk.Usage.GetCompletionTokens()),
			TotalTokens:      int(chunk.Usage.GetTotalTokens()),
		},
	}
}

// writeAdapterError maps an adapter Connect-RPC failure to the gateway-side
// OpenAI §5.1.2 envelope per BR-1.4. The mapping:
//
//	Code.DeadlineExceeded → 504 + 504_upstream_timeout
//	Code.Unavailable      → 502 + 502_upstream_unavailable
//	Code.Internal         → 502 + 502_upstream_unavailable
//	(any other / dial error / nil-Connect-Error) → 502 + 502_upstream_unavailable
//
// The user-facing collapse of 4xx/5xx → 502 is intentional: upstream auth
// failures are OUR misconfig (operator rotates DEEPSEEK_UPSTREAM_API_KEY),
// NOT the user's, so we never surface 401/403/etc as such to the SDK.
func (h *ChatCompletionsHandler) writeAdapterError(w http.ResponseWriter, ctx context.Context, req *ChatRequest, apiKeyID string, err error, fallbackStatus int) {
	status, code, message := classifyAdapterError(err)
	h.logger.LogAttrs(ctx, slog.LevelWarn, "chat_completions_adapter_error",
		slog.String("event", "chat_completions_adapter_error"),
		slog.String("model", req.Model),
		slog.String("api_key_id", apiKeyID),
		slog.Int("messages_count", len(req.Messages)),
		slog.String("error_code", code),
		slog.String("error", adapterErrString(err)),
	)
	_ = openaierr.Write(w, ctx, status, code, message, nil)
}

func adapterErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// classifyAdapterError implements the BR-1.4 canonical mapping table.
func classifyAdapterError(err error) (status int, code, message string) {
	if err == nil {
		return http.StatusBadGateway, "502_upstream_unavailable", "Upstream model service is unavailable, please retry."
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		switch connectErr.Code() {
		case connect.CodeDeadlineExceeded:
			return http.StatusGatewayTimeout, "504_upstream_timeout", "Upstream model service did not respond within the deadline."
		case connect.CodeUnavailable:
			return http.StatusBadGateway, "502_upstream_unavailable", "Upstream model service is unavailable, please retry."
		case connect.CodeInternal:
			return http.StatusBadGateway, "502_upstream_unavailable", "Upstream model service is unavailable, please retry."
		default:
			return http.StatusBadGateway, "502_upstream_unavailable", "Upstream model service is unavailable, please retry."
		}
	}
	// Non-Connect error (typically a dial / TCP failure) — treat as
	// adapter-unreachable per BR-1.4.
	return http.StatusBadGateway, "502_upstream_unavailable", "Upstream model service is unavailable, please retry."
}

// mockChatCompletionResponse builds the deterministic mock body. Per BR-4.3 the
// response depends only on the served `model` (echoed) — same input always
// produces the same output modulo id randomness + created timestamp. Story 6.2:
// `model` is the ROUTED selection (== req.Model on the default/passthrough path).
func mockChatCompletionResponse(req *ChatRequest, model, id string, now time.Time) *ChatResponse {
	return &ChatResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: now.UTC().Unix(),
		Model:   model,
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
