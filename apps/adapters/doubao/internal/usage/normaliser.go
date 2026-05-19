// Package usage owns the BR-3.2 Normaliser implementation for the
// Doubao (Volcengine Ark v3) adapter — Story 4.5. Architect Round 1
// OQ-4.5-3 ratifies OpenAI-shape usage JSON (Ark v3 OpenAI-compat) →
// Normaliser is identity-mapping (the JSON-tag names in
// upstream.RawUsage already align with OpenAI naming).
//
// Per Architect Round 1 OQ-4.2-3a partial-lift (M1) cascade:
// NormalisedUsage + ErrUsageConstraintViolation + ValidateInvariants
// are consumed from `packages/adapter-usage`. The Normaliser INTERFACE
// itself stays vendor-local (parametric on Doubao's RawUsage) — Go
// structural typing handles cross-vendor consistency without forcing
// generics.
//
// NOTE: the `model` field bidirectional rewrite per BR-1.11 is a
// SEPARATE concern handled in `internal/upstream/translate.go`. The
// identity-mapping cascade per OQ-4.5-3 applies ONLY to the `usage`
// field here.
package usage

import (
	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
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
// Round 1 ruling). The interface signature mirrors Story-4.1/4.2/4.3/4.4
// vendor-local interfaces — Go's structural typing means the adapter
// package can swap implementations without coupling.
type Normaliser interface {
	Normalise(raw upstream.RawUsage) (NormalisedUsage, error)
}

// doubaoNormaliser is the Volcengine Ark v3 OpenAI-compat identity-mapping
// implementation. Ark v3 emits OpenAI-canonical JSON field names so
// the mapping is identity (PromptTokens → PromptTokens, etc.) with the
// BR-3.3 invariant check delegated to the shared ValidateInvariants
// helper.
type doubaoNormaliser struct{}

// NewDoubao constructs a Normaliser for the Doubao (Volcengine Ark v3
// OpenAI-compat) upstream API.
func NewDoubao() Normaliser { return doubaoNormaliser{} }

// Normalise enforces the BR-3.3 invariants (via the shared helper) and
// identity-maps the raw shape onto NormalisedUsage.
func (doubaoNormaliser) Normalise(raw upstream.RawUsage) (NormalisedUsage, error) {
	if err := sharedusage.ValidateInvariants(raw.PromptTokens, raw.CompletionTokens, raw.TotalTokens); err != nil {
		return NormalisedUsage{}, err
	}
	return NormalisedUsage{
		PromptTokens:     raw.PromptTokens,
		CompletionTokens: raw.CompletionTokens,
		TotalTokens:      raw.TotalTokens,
	}, nil
}
