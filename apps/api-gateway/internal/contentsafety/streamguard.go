// Story 8.3 (AC2) — StreamGuard: the §9.3 流式出参 sliding-window detector.
//
// A streamed completion is flushed to the caller incrementally (the chunker
// emits `data: <json>\n\n` content deltas), so — unlike the non-stream redact
// path which scans an assembled body — the output guard must detect a sensitive
// term as the output ACCUMULATES, even when a term is split across delta
// boundaries (the gravest defect class: a missed 出参 term = a 备案 compliance
// breach, BR-2.5). StreamGuard wraps the PURE 8.2 Scanner and adds the only
// mutable state on the path: a bounded rolling rune buffer.
//
// Design (Architect Round-1 rulings, which SUPERSEDE the SM proposals):
//   - OQ-8.3-2 scan-before-forward: the caller calls Observe(delta) BEFORE it
//     forwards the delta; a true return means this delta COMPLETES a stored
//     term, so the caller withholds it (never flushed) and terminates. Only
//     earlier partial deltas (which do not match on their own) may have leaked.
//   - OQ-8.3-3 window = max(streamWindowFloor, max(TermLengths())): the no-FN
//     bound is `window ≥ longest term`, derived from corpus truth at
//     construction (NOT a bare magic constant) so it can never regress if the
//     corpus grows a longer term.
//   - M-3: the raw rune buffer is scanned via the SHARED Normalize-d ScanText,
//     so a zero-width-padded term split across deltas still folds to its
//     canonical and is detected (the floor gives generous headroom for padding).
//
// StreamGuard is STATEFUL and per-request: construct a fresh one per stream and
// NEVER share it across goroutines. The Scanner it wraps is pure/concurrent-safe,
// so one Scanner instance may back many concurrent guards (BLIND-CONCURRENCY).
package contentsafety

import (
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// streamWindowFloor is the §9.3 "最近 200 tokens 滑窗" approximated in RUNES
// (OQ-8.3-3): the gateway has NO tokenizer on the stream path, so a generous
// fixed rune floor stands in for the ~200-token window. The realized window is
// max(this, corpus-longest-term), so this floor only ever WIDENS the window —
// the no-false-negative bound (≥ longest term) is guaranteed by construction.
const streamWindowFloor = 512

// StreamGuard buffers a streamed completion's output and detects sensitive terms
// across chunk boundaries. The buffer is the only mutable state; once a hit is
// captured the guard latches blocked so a defensive re-Observe is idempotent.
type StreamGuard struct {
	scanner *Scanner
	window  int // max runes retained between Observe calls; ≥ longest corpus term
	buf     []rune
	blocked bool
	match   safetylexicon.Match
}

// NewStreamGuard builds a per-request guard over the supplied Scanner. The window
// is computed from corpus truth (max(streamWindowFloor, scanner max term-length))
// so it is ≥ the longest stored term — a term can never be split out of the
// window (BR-2.5 no-FN-by-construction).
func NewStreamGuard(s *Scanner) *StreamGuard {
	window := streamWindowFloor
	if mt := s.maxTermLen(); mt > window {
		window = mt
	}
	return &StreamGuard{scanner: s, window: window}
}

// Window reports the realized rune window budget (≥ max(TermLengths())). Exposed
// for the no-FN-by-construction assertion (UNIT-014).
func (g *StreamGuard) Window() int { return g.window }

// Observe appends delta to the rolling buffer and re-scans, returning the FIRST
// confirmed Match the moment a stored term becomes whole in the accumulated
// output. The contract is scan-BEFORE-forward: the caller invokes Observe before
// forwarding the delta, so a true return lets the caller WITHHOLD the
// term-completing delta and terminate (OQ-8.3-2).
//
// CRITICAL — scan-before-trim (UNIT-015): the FULL extended buffer is scanned
// BEFORE it is trimmed to the window. A trim-then-scan implementation would drop
// a term sitting in the LEADING portion of a single delta larger than the window
// → a false-negative. Trimming happens only AFTER a clean scan, retaining the
// most-recent `window` runes so a term straddling the NEXT seam is fully
// reconstructed (the retained overlap ≥ longest term).
func (g *StreamGuard) Observe(delta string) (safetylexicon.Match, bool) {
	if g.blocked {
		return g.match, true // latched — defensive idempotent re-Observe
	}
	if delta == "" {
		return safetylexicon.Match{}, false
	}
	g.buf = append(g.buf, []rune(delta)...)

	// Scan the FULL buffer (carry-over overlap + the entire new delta) BEFORE any
	// trim. ScanText applies the SHARED Normalize, so width/case/zero-width
	// evasion folds to canonical here too (M-3).
	if m, hit := g.scanner.ScanText(string(g.buf)); hit {
		g.blocked = true
		g.match = m
		return m, true
	}

	// Clean → slide the window: retain the last `window` runes (≥ longest term)
	// for the next seam; oldest runes drop so steady-state memory is O(window)
	// regardless of total output length (BR-4.4). The forward in-place copy is
	// safe because the destination start (0) precedes the source start.
	if len(g.buf) > g.window {
		n := len(g.buf) - g.window
		g.buf = append(g.buf[:0], g.buf[n:]...)
	}
	return safetylexicon.Match{}, false
}

// Blocked reports the captured Match once Observe has confirmed a hit. The
// handler reads this AFTER the chunker returns streaming.ErrContentFiltered to
// build the Story-8.5 output event (OQ-8.3-1 / M-1: the chunker writes the
// terminal; the handler records + meters).
func (g *StreamGuard) Blocked() (safetylexicon.Match, bool) {
	return g.match, g.blocked
}
