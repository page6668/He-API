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
	"log/slog"
	"net/http"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
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
	Transcription       bool `json:"transcription"` // Story 9.6 — audio-transcription (ASR) capability
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

// modelsCatalogue + capabilitiesByModelID are the BR-1.4 (Architect Round 1
// OQ1) authoritative 11-entry catalogue. As of Story 6.1 (Architect Round-1
// Q-A, option (a)) the seed data + the BR-1.3 1:1 invariant moved OUT of this
// package into the shared github.com/he-api/he-api/packages/models-catalogue
// module, so the api-gateway and the new routing-svc consume one canonical
// source of truth (no duplication, no cross-service drift). These two
// package-level vars are now RECONSTRUCTED from that shared catalogue at init
// — the wire shape (declaration order, Object="model", owned_by=vendor, the
// 7 capability fields) is preserved byte-for-byte, so the Story-4.7 contract
// (incl. 4.7-INT-001) is unchanged.
//
// IMMUTABLE: ServeHTTP emits a per-request copy via make+copy — never mutate
// these in place (BR-1.10 ordering + concurrency safety). The 1:1 invariant
// is enforced inside the shared package (panic at its init) per Story 6.1
// BR4-3; the gateway no longer carries its own init() check.
var modelsCatalogue, capabilitiesByModelID = buildGatewayCatalogue(modelscatalogue.DefaultCatalogue)

// buildGatewayCatalogue projects the shared domain catalogue into the
// gateway's OpenAI-compatible response types. The per-model `Object` is the
// constant "model" and `OwnedBy` is the shared `Vendor`; `Created` stays
// zero-valued (it is patched per-request from the handler's startedAt clock,
// BR-1.6). Declaration order is preserved from Catalogue.List() (BR-1.10).
func buildGatewayCatalogue(c modelscatalogue.Catalogue) ([]ModelEntry, map[string]ModelCapabilities) {
	entries := c.List()
	cat := make([]ModelEntry, len(entries))
	caps := make(map[string]ModelCapabilities, len(entries))
	for i, e := range entries {
		cat[i] = ModelEntry{ID: e.ID, Object: "model", OwnedBy: e.Vendor}
		caps[e.ID] = ModelCapabilities{
			Chat:                e.Capabilities.Chat,
			Streaming:           e.Capabilities.Streaming,
			FunctionCalling:     e.Capabilities.FunctionCalling,
			Vision:              e.Capabilities.Vision,
			JSONMode:            e.Capabilities.JSONMode,
			Transcription:       e.Capabilities.Transcription,
			ContextWindowTokens: e.Capabilities.ContextWindowTokens,
			MaxOutputTokens:     e.Capabilities.MaxOutputTokens,
		}
	}
	return cat, caps
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
