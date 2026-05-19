// Package usage owns the BR-3.2 Normaliser implementation for the Kimi
// (Moonshot API) adapter. Architect Round 1 OQ-4.2-1 ratifies
// OpenAI-compatible endpoint usage; OQ-4.2-3 cascade makes the Normaliser
// identity-mapping (the JSON-tag names in upstream.RawUsage already align
// with OpenAI naming).
//
// Per Architect Round 1 OQ-4.2-3a partial-lift (M1): NormalisedUsage +
// ErrUsageConstraintViolation + ValidateInvariants are consumed from
// `packages/adapter-usage`. The Normaliser INTERFACE itself stays
// vendor-local (parametric on Kimi's RawUsage) — Go structural typing
// handles cross-vendor consistency without forcing generics.
package usage

import (
	"github.com/he-api/he-api/apps/adapters/kimi/internal/upstream"
	sharedusage "github.com/he-api/he-api/packages/adapter-usage"
)

// NormalisedUsage is the post-Normaliser shape — type-aliased to the
// shared canonical shape so the adapter package builds ChatChunk.Usage
// from a single canonical type.
type NormalisedUsage = sharedusage.NormalisedUsage

// ErrUsageConstraintViolation is re-exported from the shared package
// for ergonomic test imports.
var ErrUsageConstraintViolation = sharedusage.ErrUsageConstraintViolation

// Normaliser is the BR-3.2 seam (vendor-local interface per Architect
// Round 1 ruling). The interface signature mirrors Story-4.1 deepseek's
// vendor-local interface — Go's structural typing means the adapter
// package can swap implementations without coupling.
type Normaliser interface {
	Normalise(raw upstream.RawUsage) (NormalisedUsage, error)
}

// kimiNormaliser is the Moonshot identity-mapping implementation.
// Moonshot API emits OpenAI-canonical JSON field names so the
// mapping is identity (PromptTokens → PromptTokens, etc.) with the
// BR-3.3 invariant check delegated to the shared ValidateInvariants
// helper.
type kimiNormaliser struct{}

// NewKimi constructs a Normaliser for the Kimi (Moonshot) API
// upstream API.
func NewKimi() Normaliser { return kimiNormaliser{} }

// Normalise enforces the BR-3.3 invariants (via the shared helper) and
// identity-maps the raw shape onto NormalisedUsage.
func (kimiNormaliser) Normalise(raw upstream.RawUsage) (NormalisedUsage, error) {
	if err := sharedusage.ValidateInvariants(raw.PromptTokens, raw.CompletionTokens, raw.TotalTokens); err != nil {
		return NormalisedUsage{}, err
	}
	return NormalisedUsage{
		PromptTokens:     raw.PromptTokens,
		CompletionTokens: raw.CompletionTokens,
		TotalTokens:      raw.TotalTokens,
	}, nil
}
