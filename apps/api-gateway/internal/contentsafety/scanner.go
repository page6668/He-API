package contentsafety

import (
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// lexicon is the membership surface the scanner needs from safety-lexicon.
// safetylexicon.Lexicon satisfies it; declaring it as an interface lets the
// package's own tests inject a call-counting spy to assert the Bloom-before-
// Lookup ordering (UNIT-015) without a transport or a real corpus.
type lexicon interface {
	MightContain(term string) bool
	Lookup(term string) (safetylexicon.Match, bool)
	TermLengths() []int
}

// Scanner resolves free request text against the Story-8.1 lexicon, realizing the
// §9.3 入参 Filter layers (1) Bloom 快速排除 + (2) 词典匹配 (OQ-8.2-1 option A,
// Architect-ratified). It is PURE over the immutable lexicon — no I/O, no time,
// no rand, no mutable state — so it is deterministic and safe for concurrent use
// from any number of request goroutines without locks (BR-2.5, mirrors 8.1).
type Scanner struct {
	lex lexicon
	// descLengths is the corpus's distinct rune-lengths, sorted DESCENDING, so
	// the window pass tries the longest candidate first at each position
	// (longest-match-first → a stable matched_rule under overlapping terms,
	// Architect Low #1 / UNIT-017). Bounding windows by the realized corpus
	// lengths (NOT a magic constant) is the no-false-negative invariant (BR-2.2):
	// a stored term longer than a hardcoded bound would be a silent miss.
	descLengths []int
}

// NewScanner builds a Scanner over the supplied lexicon (production wires
// safetylexicon.DefaultLexicon). The window-length set is snapshotted from the
// lexicon's corpus-truth TermLengths() at construction.
func NewScanner(l safetylexicon.Lexicon) *Scanner {
	return newScannerFromLexicon(l)
}

// newScannerFromLexicon is the interface-typed constructor the package tests use
// to inject a spy lexicon. Production calls flow through NewScanner.
func newScannerFromLexicon(l lexicon) *Scanner {
	asc := l.TermLengths()
	desc := make([]int, len(asc))
	for i, n := range asc {
		desc[len(asc)-1-i] = n // reverse the ascending TermLengths() into descending
	}
	return &Scanner{lex: l, descLengths: desc}
}

// maxTermLen reports the longest realized Canonical rune-length in the corpus
// (descLengths is sorted DESCENDING, so element 0 is the max), or 0 for an empty
// corpus. It is the no-false-negative window bound the Story-8.3 StreamGuard
// derives its buffer size from (window ≥ longest term → a stored term can never
// be split out of the window, BR-2.5).
func (s *Scanner) maxTermLen() int {
	if len(s.descLengths) == 0 {
		return 0
	}
	return s.descLengths[0]
}

// Scan resolves a slice of message contents in array order and returns the FIRST
// confirmed hit (fail-fast reject, BR-3.3). Scanning in array order + text order
// makes the verdict and the reported Match fully deterministic (BR-4.3). The
// handler passes every message's content regardless of role (all client-supplied
// → all an injection surface, BR-1.5).
//
// Scan is the block-all (== strict) entry point, preserved BYTE-IDENTICAL for
// pre-8.4 callers: Scan(contents) == ScanMin(contents, SeverityLow) (Story 8.4
// BR-2.3 / G4).
func (s *Scanner) Scan(contents []string) (safetylexicon.Match, bool) {
	return s.ScanMin(contents, safetylexicon.SeverityLow)
}

// ScanMin is the Story-8.4 severity-aware input variant of Scan: it scans the
// contents in array order and returns the FIRST AT-OR-ABOVE-threshold hit
// (rank(Severity) >= rank(min)). A sub-threshold match in an earlier message
// does NOT short-circuit and does NOT mask a later qualifying match (BR-2.3
// "first-qualifying-hit"). With min == SeverityLow every confirmed hit qualifies,
// so ScanMin(contents, SeverityLow) == Scan's pre-8.4 first-any-hit behaviour.
func (s *Scanner) ScanMin(contents []string, min safetylexicon.Severity) (safetylexicon.Match, bool) {
	for _, c := range contents {
		if m, hit := s.ScanTextMin(c, min); hit {
			return m, true
		}
	}
	return safetylexicon.Match{}, false
}

// ScanText scans one content string and returns the first confirmed Match, or
// (Match{}, false) if clean. The pipeline per the §9.3 design:
//
//  1. Normalize via the SHARED safetylexicon.Normalize (BR-2.5 / UNIT-016) so
//     producer (8.1) and consumer (8.2) normalization can never drift — this
//     folds the width / case / zero-width / padding evasion classes up front.
//  2. Slide a rune window over the normalized text at every realized corpus
//     length (descending = longest-first), for each window:
//     a. MightContain — the §9.3 layer-1 Bloom fast-exclude (BR-2.3); ~98% of
//     windows are rejected here without touching the authoritative map.
//     b. Lookup — the layer-2 dictionary confirm for Bloom-positives only.
//
// A unified rune window (rather than ASCII-punctuation tokenization) is used for
// BOTH scripts deliberately: the corpus contains terms with internal punctuation
// (e.g. "placeholder-violence_terror-0100"), so splitting on punctuation
// boundaries would shatter such a term and miss it — a false-negative, the
// gravest defect class. Windowing over the realized lengths is provably
// no-false-negative: any stored term of rune-length L appearing at position i is
// exactly the window runes[i:i+L]. See the Dev Log for the OQ-8.2-1 rationale.
//
// There is NO canonical-input fast path (Architect Medium): the redundant
// re-normalization inside MightContain/Lookup is accepted and measured by the
// soft-gate benchmark, NEVER traded for a miss (UNIT-018).
func (s *Scanner) ScanText(content string) (safetylexicon.Match, bool) {
	return s.ScanTextMin(content, safetylexicon.SeverityLow)
}

// ScanTextMin is the Story-8.4 severity-aware scan (BR-2.3 / OQ-8.4-2, Architect
// Round-1 Option A). It runs the IDENTICAL §9.3 detection pipeline as ScanText
// (shared Normalize → longest-first rune window → Bloom fast-exclude → Lookup
// confirm) but ACTS only on a confirmed Match whose severity is AT OR ABOVE the
// `min` threshold (rank(Severity) >= rank(min)). A confirmed BUT sub-threshold
// hit is SKIPPED and the scan CONTINUES, so it can never short-circuit and mask
// a later qualifying match — the fail-fast contract becomes "first QUALIFYING
// hit", not "first ANY hit".
//
// Detection is UNCHANGED: a sub-threshold term is still fully DETECTED (the
// no-false-negative scanner is untouched); ScanTextMin only decides whether a
// detected term is reported as a hit (BR-2.6). The zero-regression invariant
// holds BY CONSTRUCTION: with min == SeverityLow every severity qualifies, so
// ScanText(t) == ScanTextMin(t, SeverityLow) for ALL t (G4 / UNIT-014) and every
// existing 8.2/8.3 caller/test is unaffected.
func (s *Scanner) ScanTextMin(content string, min safetylexicon.Severity) (safetylexicon.Match, bool) {
	norm := safetylexicon.Normalize(content)
	if norm == "" {
		return safetylexicon.Match{}, false // empty / whitespace-only → no candidate
	}
	runes := []rune(norm)
	n := len(runes)
	minRank := severityRank(min)

	for i := 0; i < n; i++ {
		// Longest-first at this start position → on overlap (e.g. both "ab" and
		// "abc" stored, content "abc") the longest QUALIFYING canonical is
		// reported, pinning the matched_rule for the Story-8.5 event (Architect
		// Low #1 / UNIT-017). A longer but sub-threshold match does not block a
		// shorter qualifying one at the same position.
		for _, L := range s.descLengths {
			if i+L > n {
				continue // window would run past the end at this length
			}
			candidate := string(runes[i : i+L])
			if !s.lex.MightContain(candidate) {
				continue // §9.3 layer-1 Bloom fast-exclude
			}
			m, ok := s.lex.Lookup(candidate)
			if !ok {
				continue // Bloom false-positive — not a member
			}
			if severityRank(m.Severity) < minRank {
				continue // confirmed but BELOW threshold → detected, not acted on; keep scanning
			}
			return m, true // §9.3 layer-2 confirm AND at-or-above threshold → first qualifying hit
		}
	}
	return safetylexicon.Match{}, false
}
