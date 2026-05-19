// Package usage owns the BR-3.2 Normaliser interface that converts an
// upstream-reported RawUsage shape into the proto-canonical NormalisedUsage
// shape the gateway threads through to the SDK response. Story 4.1 ships
// the DeepSeek identity-mapping implementation; Stories 4.2-4.6 supply
// non-identity per-vendor implementations.
//
// BR-3.1 oracle rule: upstream IS the billing oracle. The adapter MUST NOT
// re-tokenise the request or response to "verify" or "correct" upstream's
// reported counts — re-tokenisation introduces our own tokeniser as a
// source of drift. The Normaliser only enforces shape invariants (BR-3.3:
// positive prompt_tokens; non-negative completion_tokens; total == prompt +
// completion), never recomputes values.
package usage

import (
	"errors"

	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
)

// ErrUsageConstraintViolation indicates the upstream-reported RawUsage
// violates BR-3.3 invariants (positive prompt; non-negative completion;
// total == prompt + completion). The adapter surfaces this as Connect-RPC
// Code.Unavailable with slog `validation_failure=usage_constraint_violation`
// per the Story 4.1 AC1 Error Handling table.
var ErrUsageConstraintViolation = errors.New("usage: BR-3.3 invariant violated")

// NormalisedUsage is the post-normalisation shape (proto-canonical OpenAI
// field names). DeepSeek's NormalisedUsage == its RawUsage by definition
// (identity-mapping); Stories 4.2-4.6 produce a NormalisedUsage from a
// vendor-shaped RawUsage where field names differ.
type NormalisedUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Normaliser is the BR-3.2 seam Stories 4.2-4.6 inherit. Each adapter
// service supplies one implementation.
type Normaliser interface {
	Normalise(raw upstream.RawUsage) (NormalisedUsage, error)
}

// deepseekNormaliser is the BR-3.2 identity-mapping implementation. DeepSeek
// returns OpenAI-canonical JSON field names; no key remapping is needed.
type deepseekNormaliser struct{}

// NewDeepSeek constructs a Normaliser for the DeepSeek upstream API.
func NewDeepSeek() Normaliser { return deepseekNormaliser{} }

// Normalise enforces the BR-3.3 invariants and identity-maps the raw shape.
func (deepseekNormaliser) Normalise(raw upstream.RawUsage) (NormalisedUsage, error) {
	if raw.PromptTokens <= 0 {
		return NormalisedUsage{}, ErrUsageConstraintViolation
	}
	if raw.CompletionTokens < 0 {
		return NormalisedUsage{}, ErrUsageConstraintViolation
	}
	if raw.TotalTokens < 0 {
		return NormalisedUsage{}, ErrUsageConstraintViolation
	}
	if raw.TotalTokens != raw.PromptTokens+raw.CompletionTokens {
		return NormalisedUsage{}, ErrUsageConstraintViolation
	}
	return NormalisedUsage{
		PromptTokens:     raw.PromptTokens,
		CompletionTokens: raw.CompletionTokens,
		TotalTokens:      raw.TotalTokens,
	}, nil
}
