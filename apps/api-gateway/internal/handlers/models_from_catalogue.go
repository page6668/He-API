package handlers

import (
	"github.com/he-api/he-api/apps/api-gateway/internal/catalogue"
)

// ModelSource returns the catalogue entries to serve right now.
//
// It is called once per request so a refresh landing in the background is
// visible immediately — the point of AD-002 is that models go live without a
// deploy, which a slice captured at construction cannot deliver. Implementations
// must be cheap and concurrency-safe (catalogue.Snapshot.Models is both: an
// RWMutex read returning an immutable slice).
//
// `Created` on the returned entries is ignored; each handler stamps its own
// process-start anchor so the bearer-gated and public endpoints agree
// (4.7-INT-001 byte-identity).
type ModelSource func() []ModelEntry

// catalogueReader is the slice of catalogue.Snapshot this package needs. Taking
// an interface keeps handlers free of a construction-order dependency on main.
type catalogueReader interface {
	Models() []catalogue.Model
}

// CatalogueSource adapts a live catalogue snapshot into a ModelSource, so both
// /v1/models and /public/models read the same rows on every request.
func CatalogueSource(snap catalogueReader) ModelSource {
	return func() []ModelEntry {
		return EntriesFromCatalogue(snap.Models(), 0)
	}
}

// EntriesFromCatalogue converts DB-backed catalogue rows into the wire shape
// /v1/models and /public/models already emit.
//
// AD-002 (specs/model-catalogue-arch.md): he_api.models is the source of truth;
// this function is the seam. The response envelope is UNCHANGED — existing
// clients and the 4.7-INT-001 byte-identity contract between the bearer-gated
// and public endpoints both keep holding. Only where the data comes from moved.
//
// `created` is the process start anchor rather than the row's created_at: the
// public mirror must match the bearer endpoint within one process, which is the
// property 4.7-INT-001 pins. Row timestamps would drift between the two.
func EntriesFromCatalogue(models []catalogue.Model, createdAt int64) []ModelEntry {
	out := make([]ModelEntry, 0, len(models))
	for _, m := range models {
		out = append(out, ModelEntry{
			ID:      m.ID,
			Object:  "model",
			Created: createdAt,
			OwnedBy: m.Vendor,
			Capabilities: ModelCapabilities{
				Chat:                m.Capabilities.Chat,
				Streaming:           m.Capabilities.Streaming,
				FunctionCalling:     m.Capabilities.FunctionCalling,
				Vision:              m.Capabilities.Vision,
				JSONMode:            m.Capabilities.JSONMode,
				Transcription:       m.Capabilities.Transcription,
				Speech:              m.Capabilities.Speech,
				ContextWindowTokens: m.Capabilities.ContextWindowTokens,
				MaxOutputTokens:     m.Capabilities.MaxOutputTokens,
			},
		})
	}
	return out
}

// CatalogueFallback adapts the compiled-in registry into catalogue rows, used
// when the database is unreachable at boot (AD-002 failure mode 2). These ids
// are known-stale — vendors retire them — so this is a last resort that keeps
// the public endpoints answering, not a substitute for the database.
func CatalogueFallback() []catalogue.Model {
	out := make([]catalogue.Model, 0, len(modelsCatalogue))
	for _, e := range modelsCatalogue {
		caps := capabilitiesByModelID[e.ID]
		out = append(out, catalogue.Model{
			ID:          e.ID,
			DisplayName: e.ID,
			Vendor:      e.OwnedBy,
			Capabilities: catalogue.Capabilities{
				Chat:                caps.Chat,
				Streaming:           caps.Streaming,
				FunctionCalling:     caps.FunctionCalling,
				Vision:              caps.Vision,
				JSONMode:            caps.JSONMode,
				Transcription:       caps.Transcription,
				Speech:              caps.Speech,
				ContextWindowTokens: caps.ContextWindowTokens,
				MaxOutputTokens:     caps.MaxOutputTokens,
			},
		})
	}
	return out
}
