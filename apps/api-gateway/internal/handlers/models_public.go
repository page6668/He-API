// Story 4.7 — GET /public/models (unauthenticated mirror of /v1/models).
//
// First non-bearer-gated endpoint on the api-gateway. Establishes the
// `/public/*` URL convention (OQ-4.7-4 ratified) that Epic-9 observability
// + Epic-10 marketing-launch endpoints (`/public/benchmark`,
// `/public/compliance`) will inherit.
//
// Architect Round 1 rulings honoured here:
//   - OQ-4.7-5 — constructor injection: NewPublicModelsHandler accepts the
//     pre-built `data []ModelEntry` snapshot, sharing it with the bearer-
//     gated ModelsHandler so both endpoints emit byte-identical bodies.
//   - OQ-4.7-6 — 405 path returns the NEW `405_method_not_allowed`
//     envelope (added to openaierr.CodeMetadata) with `Allow: GET`.
//   - BR-1.9 — slog `event=models_list_public`; PII discipline: no
//     api_key_id (the route is OUTSIDE the bearer middleware chain so no
//     credential context exists to leak). m-2 advisory `remote_addr` is
//     OPTIONAL in Round 1; included here for DDoS/abuse correlation as it
//     is the originator IP, not a user attribute (non-PII).
package handlers

import (
	"log/slog"
	"net/http"

	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// PublicModelsHandler serves GET /public/models. It is intentionally
// minimal: a pre-built snapshot built ONCE at construction is emitted on
// every request via the same writeChatJSON path used by ModelsHandler.
// The snapshot is immutable after construction (no field mutation) so the
// per-request slice copy discipline of Story 3.5 collapses to a write of
// the same bytes — concurrency-safe without further locking.
type PublicModelsHandler struct {
	logger   *slog.Logger
	snapshot []ModelEntry
}

// NewPublicModelsHandler builds a handler from a pre-built snapshot. The
// caller (main.go) typically derives the snapshot from BuildPublicModelsSnapshot
// so both endpoints (`/v1/models` and `/public/models`) emit byte-identical
// bodies sharing the same `created` timestamp.
//
// logger may be nil — falls back to slog.Default().
func NewPublicModelsHandler(logger *slog.Logger, snapshot []ModelEntry) *PublicModelsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	// Defensive copy — the caller's slice MAY be the same backing array
	// the bearer-gated handler emits each request; we want to be insulated
	// from any future mutation upstream of the constructor.
	cp := make([]ModelEntry, len(snapshot))
	copy(cp, snapshot)
	return &PublicModelsHandler{logger: logger, snapshot: cp}
}

// BuildPublicModelsSnapshot constructs the `data []ModelEntry` slice that
// the unauthenticated endpoint emits. The `created` value comes from the
// bearer-gated handler so both endpoints carry the same timestamp (4.7-INT-001
// byte-identity claim).
func BuildPublicModelsSnapshot(createdAt int64) []ModelEntry {
	out := make([]ModelEntry, len(modelsCatalogue))
	for i := range modelsCatalogue {
		out[i] = modelsCatalogue[i]
		out[i].Created = createdAt
		out[i].Capabilities = capabilitiesByModelID[modelsCatalogue[i].ID]
	}
	return out
}

// ServeHTTP implements http.Handler.
//
// Method gate: only GET (and HEAD, which Go's stdlib mux treats as GET
// with body suppression) is allowed. Any other method emits the
// canonical §5.1.2 `405_method_not_allowed` envelope with `Allow: GET`
// per RFC 7231 §6.5.5. The handler is mounted OUTSIDE the bearer
// middleware chain in main.go — there is no `APIKeyID` context to
// consult; rogue Authorization headers are silently ignored.
func (h *PublicModelsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		_ = openaierr.Write(w, r.Context(), http.StatusMethodNotAllowed,
			"405_method_not_allowed",
			"Only GET is allowed on /public/models.", nil)
		return
	}

	// BR-1.9 — exactly one structured log line per accepted request.
	// PII discipline: NO api_key_id (the route is unauthenticated; no
	// bearer context exists). m-2 — `remote_addr` is included as a
	// non-PII abuse-correlation field; this is the originator's IP,
	// not a stored user attribute.
	h.logger.InfoContext(
		r.Context(), "models_list_public",
		slog.String("event", "models_list_public"),
		slog.String("remote_addr", r.RemoteAddr),
		slog.Int("catalogue_size", len(h.snapshot)),
	)

	writeChatJSON(w, http.StatusOK, ModelsResponse{
		Object: "list",
		Data:   h.snapshot,
	})
}
