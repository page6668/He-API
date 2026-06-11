// Story 8.4 — per-Key 内容安全严格度 (Epic-8 DoD "严格度可按 Key 配置").
//
// This file is the PURE severity-gating seam: it owns the user-facing strictness
// LEVELS {strict, default, loose}, their mapping onto the Story-8.1 lexicon
// severity threshold, and the post-detection block decision. It has NO I/O, no
// time, no rand, no mutable state — so it is deterministic and safe for
// concurrent use (mirrors the scanner / lexicon posture).
//
// The strictness LEVELS are a DIFFERENT closed set from the lexicon SEVERITIES
// {high, medium, low}: the level is the per-Key knob, severity is the per-term
// metadata. The mapping (8.1 OQ-8.1-5, RATIFIED, reserved-for-8.4 by 8.1 BR-1.4,
// confirmed by 8.4 Architect Round-1):
//
//	loose   → minSeverity = high    (block high only)
//	default → minSeverity = medium  (block high + medium)
//	strict  → minSeverity = low     (block all three — == the shipped 8.2/8.3 posture)
//
// Block decision: a confirmed Match BLOCKS iff rank(match.Severity) >=
// rank(minSeverity(level)), with rank high(3) > medium(2) > low(1). Detection is
// NEVER weakened by this gate — a sub-threshold match is still fully DETECTED by
// the scanner; 8.4 only changes whether a detected match is ACTED ON (BR-2.6).
package contentsafety

import (
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// Strictness is the per-Key 内容安全 level — the closed user-facing enum
// {strict, default, loose} persisted on api_keys.content_safety_strictness and
// carried onto the hot path via the bearer CachedClaims.
type Strictness string

const (
	// Strict blocks all three severities (high+medium+low) — byte-identical to
	// the pre-8.4 block-all behaviour Stories 8.2/8.3 ship. It is the
	// fail-closed / zero-regression baseline: absent/empty/unknown → Strict.
	Strict Strictness = "strict"
	// Default blocks high+medium (lets low through).
	Default Strictness = "default"
	// Loose blocks high only (lets medium+low through).
	Loose Strictness = "loose"
)

// ParseStrictness resolves a raw level token to a Strictness, FAILING CLOSED to
// Strict for anything that is not one of the three EXACT lowercase tokens
// (BR-3.4). An empty string (a pre-8.4 omitempty cache entry), an unknown token,
// a severity token ("high"), whitespace-padded or mixed-case input — ALL resolve
// to Strict. The only way to a relaxed (default/loose) posture is an explicit,
// validated, persisted column value. This is the gravest-defect guard: a relaxed
// gate from a corrupt/absent value would silently weaken a live 备案 filter.
func ParseStrictness(s string) Strictness {
	switch Strictness(s) {
	case Default:
		return Default
	case Loose:
		return Loose
	default:
		// strict, "", unknown, mixed-case, whitespace-padded → fail-closed.
		return Strict
	}
}

// severityRank is the total order over the closed lexicon severity enum:
// high(3) > medium(2) > low(1). An unrecognised severity ranks 0 (below low) so
// it can never satisfy a threshold by accident — but the lexicon validates the
// closed enum at construction, so the default arm is unreachable in practice.
func severityRank(sev safetylexicon.Severity) int {
	switch sev {
	case safetylexicon.SeverityHigh:
		return 3
	case safetylexicon.SeverityMedium:
		return 2
	case safetylexicon.SeverityLow:
		return 1
	default:
		return 0
	}
}

// MinSeverity maps a strictness level onto the minimum lexicon severity that
// blocks at that level (BR-2.1): Loose→High, Default→Medium, Strict→Low. It is
// total over the closed strictness enum; any non-relaxed value (incl. an
// out-of-band Strictness) maps to SeverityLow (block-all), preserving the
// fail-closed posture.
func MinSeverity(level Strictness) safetylexicon.Severity {
	switch level {
	case Loose:
		return safetylexicon.SeverityHigh
	case Default:
		return safetylexicon.SeverityMedium
	default: // Strict (and any unexpected value) → block-all
		return safetylexicon.SeverityLow
	}
}

// Blocks reports whether a matched term of the given severity meets the level's
// threshold and must therefore be ACTED ON (BR-2.1). The comparison is the pure
// rank inequality rank(sev) >= rank(MinSeverity(level)) — INCLUSIVE at the
// threshold (a medium term under default blocks; a medium term under loose does
// not). Same (sev, level) → same verdict, always (BR-4.3).
func Blocks(sev safetylexicon.Severity, level Strictness) bool {
	return severityRank(sev) >= severityRank(MinSeverity(level))
}
