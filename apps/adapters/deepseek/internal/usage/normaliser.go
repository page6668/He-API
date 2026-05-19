// Package usage owns the BR-3.2 Normaliser interface that converts an
// upstream-reported RawUsage shape into the proto-canonical NormalisedUsage
// shape the gateway threads through to the SDK response. Story 4.1 shipped
// the DeepSeek identity-mapping implementation; Stories 4.2-4.6 supply
// non-identity per-vendor implementations.
//
// Story 4.2 retrofit (Architect Round 1 OQ-4.2-3a partial-lift, M1):
//
//	NormalisedUsage + ErrUsageConstraintViolation + the BR-3.3 invariant
//	check have been LIFTED to packages/adapter-usage so all six Epic 4
//	adapters share a single canonical post-Normaliser shape. The lift is
//	back-compat: this package re-exports the types as aliases, so all
//	existing Story 4.1 consumers (apps/adapters/deepseek/internal/adapter.go)
//	continue to compile and reference the same underlying type.
//
// BR-3.1 oracle rule: upstream IS the billing oracle. The adapter MUST NOT
// re-tokenise the request or response to "verify" or "correct" upstream's
// reported counts.
package usage

import (
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	sharedusage "github.com/he-api/he-api/packages/adapter-usage"
)

// ErrUsageConstraintViolation is re-exported from the shared package so
// existing callers (Story 4.1 adapter.go) continue to reference the same
// sentinel without import churn.
var ErrUsageConstraintViolation = sharedusage.ErrUsageConstraintViolation

// NormalisedUsage is a type-alias to the shared post-Normaliser shape.
// Story 4.1 code that builds a `usage.NormalisedUsage{...}` literal
// continues to compile unchanged (alias = same underlying type).
type NormalisedUsage = sharedusage.NormalisedUsage

// Normaliser is the BR-3.2 seam Stories 4.2-4.6 inherit. The interface
// stays vendor-local (parametric on the vendor RawUsage type) per
// Architect Round 1 ruling — Go structural typing handles cross-vendor
// consistency without forcing generics.
type Normaliser interface {
	Normalise(raw upstream.RawUsage) (NormalisedUsage, error)
}

// deepseekNormaliser is the BR-3.2 identity-mapping implementation. DeepSeek
// returns OpenAI-canonical JSON field names; no key remapping is needed.
type deepseekNormaliser struct{}

// NewDeepSeek constructs a Normaliser for the DeepSeek upstream API.
func NewDeepSeek() Normaliser { return deepseekNormaliser{} }

// Normalise enforces the BR-3.3 invariants (via the shared helper) and
// identity-maps the raw shape.
func (deepseekNormaliser) Normalise(raw upstream.RawUsage) (NormalisedUsage, error) {
	if err := sharedusage.ValidateInvariants(raw.PromptTokens, raw.CompletionTokens, raw.TotalTokens); err != nil {
		return NormalisedUsage{}, err
	}
	return NormalisedUsage{
		PromptTokens:     raw.PromptTokens,
		CompletionTokens: raw.CompletionTokens,
		TotalTokens:      raw.TotalTokens,
	}, nil
}
