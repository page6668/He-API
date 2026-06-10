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

// Scan resolves a slice of message contents in array order and returns the FIRST
// confirmed hit (fail-fast reject, BR-3.3). Scanning in array order + text order
// makes the verdict and the reported Match fully deterministic (BR-4.3). The
// handler passes every message's content regardless of role (all client-supplied
// → all an injection surface, BR-1.5).
func (s *Scanner) Scan(contents []string) (safetylexicon.Match, bool) {
	for _, c := range contents {
		if m, hit := s.ScanText(c); hit {
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
	norm := safetylexicon.Normalize(content)
	if norm == "" {
		return safetylexicon.Match{}, false // empty / whitespace-only → no candidate
	}
	runes := []rune(norm)
	n := len(runes)

	for i := 0; i < n; i++ {
		// Longest-first at this start position → on overlap (e.g. both "ab" and
		// "abc" stored, content "abc") the longest canonical is reported, pinning
		// the matched_rule for the Story-8.5 event (Architect Low #1 / UNIT-017).
		for _, L := range s.descLengths {
			if i+L > n {
				continue // window would run past the end at this length
			}
			candidate := string(runes[i : i+L])
			if !s.lex.MightContain(candidate) {
				continue // §9.3 layer-1 Bloom fast-exclude
			}
			if m, ok := s.lex.Lookup(candidate); ok {
				return m, true // §9.3 layer-2 authoritative confirm → first hit
			}
		}
	}
	return safetylexicon.Match{}, false
}
