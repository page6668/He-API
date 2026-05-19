// Package usage carries the cross-adapter shape & invariant types lifted
// from apps/adapters/deepseek/internal/usage per Story 4.2 Architect Round 1
// OQ-4.2-3a partial-lift ruling (M1).
//
// Scope of the lift (Architect ruling, NOT the full Normaliser interface):
//
//   - NormalisedUsage struct — proto-canonical OpenAI field names
//     (prompt_tokens / completion_tokens / total_tokens). Adapter Service
//     code in every vendor builds a NormalisedUsage as the post-Normaliser
//     value passed onto the wire (ChatChunk.Usage).
//   - ErrUsageConstraintViolation sentinel — BR-3.3 violation signal.
//   - ValidateInvariants helper — the BR-3.3 check extracted as a
//     standalone function so per-vendor Normalisers can call it without
//     re-duplicating the four-line if/return cascade.
//
// The Normaliser INTERFACE itself stays vendor-local because it is
// parametric on each vendor's RawUsage (Qwen's compat-mode RawUsage has
// JSON tags that already align with OpenAI; native-shape vendors emit
// different JSON tag names that map post-rename to the same canonical
// shape). Forcing a single shared Normaliser interface here would either
// require Go generics (a typed interface) or an empty-interface RawUsage
// argument (loses static type safety). Per Architect Round 1 ruling, the
// per-vendor Normaliser interface is the right granularity — Go's
// structural typing handles cross-vendor consistency without coupling.
package usage

import "errors"

// ErrUsageConstraintViolation indicates the upstream-reported usage shape
// violates the BR-3.3 invariants (positive prompt_tokens; non-negative
// completion_tokens; non-negative total_tokens; total == prompt +
// completion). The adapter surfaces this as Connect-RPC Code.Unavailable
// with slog `validation_failure=usage_constraint_violation` per the
// per-Story AC Error Handling tables.
//
// REUSE: Story 4.1 deepseek/internal/usage re-exports this sentinel via a
// type-alias for back-compat (Story 4.2 partial-lift retrofit).
var ErrUsageConstraintViolation = errors.New("usage: BR-3.3 invariant violated")

// NormalisedUsage is the post-normalisation shape (proto-canonical
// OpenAI field names) that flows from per-vendor Normalisers into the
// Connect-RPC ChatChunk.Usage protobuf message.
//
// All fields use signed int because the wire-side protobuf Usage carries
// int32; values are constrained non-negative by ValidateInvariants. The
// wire conversion to int32 happens at the adapter boundary; here we stay
// in `int` for ergonomic arithmetic (BR-3.3 sum invariant uses plain +).
type NormalisedUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ValidateInvariants enforces BR-3.3 across all vendor Normalisers:
//
//	(1) prompt_tokens MUST be strictly positive — a successful chat
//	    completion always consumed at least one input token (every chat
//	    has at least one user-content turn).
//	(2) completion_tokens MUST be non-negative — `0` is permitted (e.g.,
//	    max_tokens=0 short-circuit; refusal with empty content).
//	(3) total_tokens MUST be non-negative.
//	(4) total_tokens MUST exactly equal prompt_tokens + completion_tokens.
//	    Any derived-field drift (vendor returning a total that does not
//	    match the sum) is treated as malformed upstream response —
//	    surface as a HARD failure so billing-svc never receives a
//	    half-counted record.
//
// Returns ErrUsageConstraintViolation on any breach; nil on success.
func ValidateInvariants(prompt, completion, total int) error {
	if prompt <= 0 {
		return ErrUsageConstraintViolation
	}
	if completion < 0 {
		return ErrUsageConstraintViolation
	}
	if total < 0 {
		return ErrUsageConstraintViolation
	}
	if total != prompt+completion {
		return ErrUsageConstraintViolation
	}
	return nil
}
