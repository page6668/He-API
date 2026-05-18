// Story 3.5 — GET /v1/models handler (static catalogue).
//
// Emits the OpenAI-compatible ModelList shape so the OpenAI Python SDK's
// client.models.list() can be exercised end-to-end before Epic 4's DB-backed
// model registry (Story 4.7) lands.
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
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// ModelEntry is one row in the /v1/models response data array. Field order
// is the OpenAI canonical shape (id → object → created → owned_by);
// encoding/json marshals in struct field declaration order.
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
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

	// BR-1.8 — exactly one log line per accepted request. PII discipline:
	// /v1/models is parameterless, so there is no user content to redact;
	// the structured fields are size signals only.
	h.logger.InfoContext(
		ctx, "models_list",
		slog.String("event", "models_list"),
		slog.String("api_key_id", apiKeyID),
		slog.Int("catalogue_size", len(modelsCatalogue)),
	)

	// BR-1.6 + BR-1.10 — per-request copy with Created patched in. The
	// package-level slice stays immutable across concurrent requests; the
	// per-request copy is the load-bearing concurrency-safety mechanism.
	data := make([]ModelEntry, len(modelsCatalogue))
	for i := range modelsCatalogue {
		data[i] = modelsCatalogue[i]
		data[i].Created = h.startedAt
	}

	writeChatJSON(w, http.StatusOK, ModelsResponse{
		Object: "list",
		Data:   data,
	})
}
