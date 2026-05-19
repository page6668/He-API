// Story 3.5 — GET /v1/models handler (static catalogue).
// Story 4.7 — extended with a non-null `capabilities` sub-struct on every
// entry; sibling unauthenticated mirror lives in models_public.go.
//
// Emits the OpenAI-compatible ModelList shape so the OpenAI Python SDK's
// client.models.list() can be exercised end-to-end. The Story-4.7
// `capabilities` field is a He-API extension appended LAST (BR-1.4 — keeps
// OpenAI canonical fields id/object/created/owned_by in declaration order so
// SDK consumers using strict-order parsers continue to round-trip cleanly).
//
// Architect Round 1 OQ1 ruling: the canonical catalogue contains 11 entries
// (NOT 9). qwen-plus + doubao-lite are required so the SDK contract surface
// matches Epic-4 Stories 4.2 / 4.5 deliverables.
//
// BR-1.6: the `created` timestamp is captured ONCE at handler construction
// and is identical across every entry on every request — model creation is a
// vendor event, not a request event. WithModelsNow injects a deterministic
// clock for the AC1 stable-`created` regression test.
//
// BR-1.10: the response `data` array is emitted in declaration order
// verbatim — SDK consumers MAY use index-based assertions; sorting by id /
// owned_by would obscure the canonical ordering.
package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// ModelCapabilities is the Story-4.7 He-API extension surfaced on every
// ModelEntry. Field declaration order MUST match BR-1.2 verbatim (the JSON
// marshaller emits fields in declaration order; Architect Round 1 OQ-4.7-3
// ratified the 11-row table that BR-1.3 commits to).
type ModelCapabilities struct {
	Chat                bool `json:"chat"`
	Streaming           bool `json:"streaming"`
	FunctionCalling     bool `json:"function_calling"`
	Vision              bool `json:"vision"`
	JSONMode            bool `json:"json_mode"`
	ContextWindowTokens int  `json:"context_window_tokens"`
	MaxOutputTokens     int  `json:"max_output_tokens"`
}

// ModelEntry is one row in the /v1/models response data array. Field order
// is the OpenAI canonical shape (id → object → created → owned_by) with the
// He-API `capabilities` extension appended LAST (Story-4.7 BR-1.4).
type ModelEntry struct {
	ID           string            `json:"id"`
	Object       string            `json:"object"`
	Created      int64             `json:"created"`
	OwnedBy      string            `json:"owned_by"`
	Capabilities ModelCapabilities `json:"capabilities"`
}

// ModelsResponse wraps the catalogue in the OpenAI ModelList envelope.
type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// modelsCatalogue is the BR-1.4 (Architect Round 1 OQ1) authoritative list
// of 11 entries. Future Story 4.7 replaces this with a DB-backed registry
// derived from he_api.models seeds; until then, this slice is the single
// source of truth for the gateway's advertised models.
//
// IMMUTABLE: ServeHTTP MUST emit a per-request copy via make+copy — never
// mutate this slice in place (BR-1.10 ordering + concurrency safety).
var modelsCatalogue = []ModelEntry{
	{ID: "qwen-max", Object: "model", OwnedBy: "alibaba"},
	{ID: "qwen-plus", Object: "model", OwnedBy: "alibaba"},
	{ID: "deepseek-v3", Object: "model", OwnedBy: "deepseek"},
	{ID: "moonshot-v1-128k", Object: "model", OwnedBy: "moonshot"},
	{ID: "glm-4", Object: "model", OwnedBy: "zhipu"},
	{ID: "doubao-pro", Object: "model", OwnedBy: "bytedance"},
	{ID: "doubao-lite", Object: "model", OwnedBy: "bytedance"},
	{ID: "ernie-4.0", Object: "model", OwnedBy: "baidu"},
	{ID: "he-router-cost", Object: "model", OwnedBy: "he-api"},
	{ID: "he-router-quality", Object: "model", OwnedBy: "he-api"},
	{ID: "he-router-latency", Object: "model", OwnedBy: "he-api"},
}

// capabilitiesByModelID is the Story-4.7 BR-1.3 authoritative capability
// table. Architect Round 1 OQ-4.7-3 ratified the 11-row values verbatim:
// per-vendor numbers are sourced from each adapter's
// docs/dev/secrets/{vendor}-upstream.md runbook; the three he-router-*
// virtual entries report the UNION of their candidate pool (marketing-
// correct — Epic-6 routing-svc gates "don't route to doubao-lite if tool-
// calling requested" at routing time, so the marketing capability surface
// is the strict superset).
//
// IMMUTABLE: the package-level map is read-only after init() — the per-
// request loop in ServeHTTP reads by key without mutation (Story-3.5 BR-
// 1.10 concurrency posture preserved).
var capabilitiesByModelID = map[string]ModelCapabilities{
	"qwen-max":          {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
	"qwen-plus":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
	"deepseek-v3":       {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 65536, MaxOutputTokens: 8192},
	"moonshot-v1-128k":  {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
	"glm-4":             {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
	"doubao-pro":        {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
	"doubao-lite":       {Chat: true, Streaming: true, FunctionCalling: false, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 4096},
	"ernie-4.0":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 8192, MaxOutputTokens: 2048},
	"he-router-cost":    {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
	"he-router-quality": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
	"he-router-latency": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
}

// init() enforces the BR-1.3 1:1 invariant between modelsCatalogue and
// capabilitiesByModelID. A drift here would surface a missing capability
// row in the JSON response (zero-value ModelCapabilities{} silently — every
// flag is false, every numeric is 0 — which would mislead SDK consumers).
// Panicking at boot is the safest mode: K8s readiness probe stays red
// until the catalogue is fixed. 4.7-UNIT-010 asserts the same invariant
// as a Go test so the failure surfaces clearly in CI before the binary
// ships.
func init() {
	if len(modelsCatalogue) != len(capabilitiesByModelID) {
		panic(fmt.Sprintf(
			"handlers: BR-1.3 invariant violated — len(modelsCatalogue)=%d, len(capabilitiesByModelID)=%d",
			len(modelsCatalogue), len(capabilitiesByModelID),
		))
	}
	for _, m := range modelsCatalogue {
		if _, ok := capabilitiesByModelID[m.ID]; !ok {
			panic("handlers: BR-1.3 invariant violated — missing capabilities row for model id " + m.ID)
		}
	}
}

// ModelsHandler serves GET /v1/models. Construct once at startup and reuse
// across all bearer-protected requests; the handler is stateless beyond the
// startedAt timestamp captured at construction.
type ModelsHandler struct {
	logger    *slog.Logger
	now       func() time.Time
	startedAt int64 // Unix seconds, captured ONCE in NewModelsHandler (BR-1.6)
}

// ModelsHandlerOption customizes a ModelsHandler at construction time.
// Production omits all options; tests pass WithModelsNow(stub) for a
// deterministic clock.
type ModelsHandlerOption func(*ModelsHandler)

// WithModelsNow replaces the default time source with f. Distinct identifier
// from handlers.WithNow (chat_completions.go) to avoid the package-level
// symbol collision called out by Architect Round 1 m-1. Nil is silently
// ignored.
func WithModelsNow(f func() time.Time) ModelsHandlerOption {
	return func(h *ModelsHandler) {
		if f != nil {
			h.now = f
		}
	}
}

// StartedAt returns the Unix-seconds timestamp captured at handler
// construction. Exposed so the Story-4.7 sibling PublicModelsHandler can
// share the same `created` value across endpoints (4.7-INT-001 byte-identity
// claim — OQ-4.7-5 constructor-injection sharing mechanism).
func (h *ModelsHandler) StartedAt() int64 { return h.startedAt }

// NewModelsHandler builds the handler. logger may be nil — falls back to
// slog.Default(). The startedAt timestamp is captured exactly once, after
// option application (so WithModelsNow precedes the capture per BR-1.6).
func NewModelsHandler(logger *slog.Logger, opts ...ModelsHandlerOption) *ModelsHandler {
	h := &ModelsHandler{
		logger: logger,
		now:    time.Now,
	}
	if h.logger == nil {
		h.logger = slog.Default()
	}
	for _, opt := range opts {
		opt(h)
	}
	h.startedAt = h.now().UTC().Unix()
	return h
}

// ServeHTTP implements http.Handler. Bearer-auth middleware (Story 3.2)
// must have already populated the request context with APIKeyID; the
// defence-in-depth check at the top surfaces a 500 if the wrap regresses
// (BR-1.7; precedent: chat_completions.go:213-219).
func (h *ModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	apiKeyID, ok := middleware.APIKeyIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured",
			"Bearer-auth middleware not wired", nil)
		return
	}

	// Story-4.7 BR-1.9 — exactly one log line per accepted request with
	// event=models_list_v1 (renamed from Story-3.5's `models_list` so the
	// bearer-gated and unauthenticated public-mirror surfaces can be
	// filtered separately in dashboards). PII discipline: /v1/models is
	// parameterless; the structured fields are size signals only.
	h.logger.InfoContext(
		ctx, "models_list_v1",
		slog.String("event", "models_list_v1"),
		slog.String("api_key_id", apiKeyID),
		slog.Int("catalogue_size", len(modelsCatalogue)),
	)

	// BR-1.6 + BR-1.10 — per-request copy with Created patched in. The
	// package-level slice stays immutable across concurrent requests; the
	// per-request copy is the load-bearing concurrency-safety mechanism.
	// Story-4.7 T1.1 — capabilities populated from the package-level map
	// inside the same loop (single map lookup per entry; in-memory; no
	// extra allocation beyond the existing per-request copy).
	data := make([]ModelEntry, len(modelsCatalogue))
	for i := range modelsCatalogue {
		data[i] = modelsCatalogue[i]
		data[i].Created = h.startedAt
		data[i].Capabilities = capabilitiesByModelID[modelsCatalogue[i].ID]
	}

	writeChatJSON(w, http.StatusOK, ModelsResponse{
		Object: "list",
		Data:   data,
	})
}
