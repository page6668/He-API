// Package modelscatalogue is the single source of truth for the He-API model
// catalogue. It was lifted out of the api-gateway in Story 6.1 (Architect
// Round-1 Q-A, option (a)) so BOTH the api-gateway (`/v1/models` +
// `/public/models`) AND the new routing-svc consume one canonical registry —
// no duplication, no cross-service drift.
//
// The package carries pure domain types only (no JSON tags, no HTTP/proto
// concerns): the api-gateway maps these into its OpenAI-compatible response
// shape, and routing-svc feeds `List()` into the decision engine as the
// candidate set. The Story-4.7 BR-1.3 1:1 invariant (every model has exactly
// one capability row, and vice-versa) moved WITH the data into this package
// (Story 6.1 BR4-3) and is enforced at construction time via panic — the same
// fail-fast posture Story 4.7 used in the gateway's init().
package modelscatalogue

import "fmt"

// Capabilities is the per-model capability matrix. Field set + semantics are
// carried over verbatim from the Story-4.7 gateway `ModelCapabilities` type
// (Architect Round-1 OQ-4.7-3 ratified values). No JSON tags here — the
// api-gateway owns the wire encoding via its own tagged mirror type.
type Capabilities struct {
	Chat            bool
	Streaming       bool
	FunctionCalling bool
	Vision          bool
	JSONMode        bool
	// Transcription (Story 9.6) — the model accepts an audio file at
	// POST /v1/audio/transcriptions (Whisper-compatible ASR). All existing
	// chat/vision models are Transcription:false (proto/Go zero value); the
	// ASR id (doubao-asr) is Chat:false, Transcription:true. The gateway gates
	// /v1/audio/transcriptions on Transcription==true and /v1/chat/completions
	// on Chat==true (the BR-1.6 do-not-regress fence).
	Transcription       bool
	ContextWindowTokens int
	MaxOutputTokens     int
}

// ModelEntry is one fully-resolved catalogue row: the model id, its owning
// vendor, and its capability matrix.
//
// DisplayName is part of the cascade-locked shape but has no data source in
// the Story-4.7 lift (the gateway never carried a human display name); it is
// reserved-for-future and left empty by DefaultRegistry. A later story may
// populate it without a contract change.
type ModelEntry struct {
	ID           string
	DisplayName  string
	Vendor       string
	Capabilities Capabilities
}

// Catalogue is an immutable, ordered view over the registry. It is safe for
// concurrent reads: List returns a fresh copy and Find reads a read-only map,
// so callers cannot mutate the shared backing data (Story 6.1 BLIND-CONCURRENCY).
type Catalogue struct {
	entries []ModelEntry          // registry-declaration order (load-bearing — see BR-1.10)
	byID    map[string]ModelEntry // O(1) lookup for Find
}

// NewFromRegistry materialises a Catalogue from a Registry, enforcing the
// BR-1.3 1:1 invariant in BOTH directions and rejecting malformed seeds. It
// panics — never returns a partially-loaded catalogue — because a drift here
// is a build-time programming error, not a runtime/config condition:
//
//   - a model seed with an empty ID                 (Story 6.1 BLIND-ERROR-002);
//   - a model with no capability entry              (Story 6.1 UNIT-042 / 4.7 BR-1.3);
//   - a capability entry with no matching model     (Story 6.1 BLIND-DATA-001, reverse 1:1).
//
// An EMPTY registry (zero models) is NOT a panic — it produces an empty
// catalogue. Rejecting an empty catalogue is the consumer's boot-time job
// (routing-svc fails fast in NewEngine per BR1-2); the gateway never ships an
// empty seed. This keeps the "programmer error → panic" and "operational
// empty → fail-fast error" paths distinct (Story 6.1 UNIT-001 vs UNIT-042).
func NewFromRegistry(reg Registry) Catalogue {
	// Forward direction: every model must have a capability row.
	entries := make([]ModelEntry, 0, len(reg.Models))
	byID := make(map[string]ModelEntry, len(reg.Models))
	for _, m := range reg.Models {
		if m.ID == "" {
			panic("models-catalogue: model seed has empty ID")
		}
		caps, ok := reg.Capabilities[m.ID]
		if !ok {
			panic(fmt.Sprintf("models-catalogue: model %q has no capability entry", m.ID))
		}
		if _, dup := byID[m.ID]; dup {
			panic(fmt.Sprintf("models-catalogue: duplicate model id %q", m.ID))
		}
		entry := ModelEntry{
			ID:           m.ID,
			DisplayName:  m.DisplayName,
			Vendor:       m.Vendor,
			Capabilities: caps,
		}
		entries = append(entries, entry)
		byID[m.ID] = entry
	}

	// Reverse direction: every capability row must map to a declared model
	// (no orphan — Story 6.1 BLIND-DATA-001).
	for id := range reg.Capabilities {
		if _, ok := byID[id]; !ok {
			panic(fmt.Sprintf("models-catalogue: capability entry %q has no matching model", id))
		}
	}

	return Catalogue{entries: entries, byID: byID}
}

// List returns the catalogue entries in registry-declaration order. The result
// is a fresh copy on every call: callers may sort/filter it freely without
// affecting the shared catalogue or any concurrent reader.
func (c Catalogue) List() []ModelEntry {
	out := make([]ModelEntry, len(c.entries))
	copy(out, c.entries)
	return out
}

// Find returns the entry for modelID and whether it exists.
func (c Catalogue) Find(modelID string) (ModelEntry, bool) {
	e, ok := c.byID[modelID]
	return e, ok
}

// Len reports the number of models in the catalogue.
func (c Catalogue) Len() int { return len(c.entries) }
