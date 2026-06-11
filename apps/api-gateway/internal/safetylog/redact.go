package safetylog

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxExcerptRunes caps excerpt_redacted (OQ-8.5-2, Architect APPROVED). A bounded
// window is better 备案 evidence than NULL while staying small enough that no
// meaningful raw context could survive even if the masking missed something.
const maxExcerptRunes = 64

// maskRune is the fixed mask glyph. A run of maskRune proves "a span of N runes
// was here" WITHOUT revealing what it was (no-lexicon-leak + 不出境, BR-1.4).
const maskRune = '■'

// PII patterns masked in the surrounding context window (BR-1.4). Ordered email →
// @handle → long-digit so the broader email pattern claims the '@' before the
// bare-handle pattern, and digit runs inside an already-masked email are not
// double-processed. Each match is replaced by maskRune×rune-count so the output
// carries neither the raw value nor (for the literal term) the surface form.
var (
	piiEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	piiHandle = regexp.MustCompile(`@[A-Za-z0-9_]{2,}`)
	// 6+ digit run, optionally broken by spaces/hyphens (phone / id / card).
	piiDigits = regexp.MustCompile(`[0-9](?:[0-9\s\-]{4,})[0-9]`)
)

// RedactExcerpt produces a 备案-safe masked excerpt from a raw context `window`
// and the matched surface span `match`. It (1) replaces every occurrence of the
// matched span with maskRune×rune-count, (2) masks residual PII (emails,
// @handles, long digit runs) in the surrounding context, and (3) clamps the
// result to maxExcerptRunes runes, centred on the masked span so it is always
// retained. The literal `match` NEVER appears in the output and no raw PII
// survives — these are the HARD redaction-safety invariants (BR-1.4 / AC4 / the
// 8.5-UNIT-006 property test).
//
// An empty window yields "" — the recorder persists that as SQL NULL, the
// conservative BR-1.4 fallback (a valid column state, 8.5-BLIND-BOUNDARY-002).
//
// NOTE: the SafetyEvent seam (BR-1.5) carries no raw context window, so the v1
// production caller passes window == match == the canonical matched_rule (see
// maskedMatchExcerpt). RedactExcerpt is the full policy, ready for a future story
// that carries a real window into the event without re-deriving the masking.
func RedactExcerpt(window, match string) string {
	if window == "" {
		return ""
	}
	out := window
	if match != "" {
		out = strings.ReplaceAll(out, match, mask(utf8.RuneCountInString(match)))
	}
	out = maskAll(piiEmail, out)
	out = maskAll(piiHandle, out)
	out = maskAll(piiDigits, out)
	return clampWindow(out, maxExcerptRunes)
}

// maskedMatchExcerpt builds the v1 excerpt from the canonical matched_rule alone.
// The SafetyEvent seam carries no raw user text (BR-1.5: 8.5 adds no field and
// changes no call site), so the excerpt is the matched span masked to
// maskRune×rune-count — proving "a match of N runes was acted on" while storing
// neither raw user content nor the literal 敏感词 (the canonical id is itself a
// non-surface rule key, and is masked here regardless). Returns "" (→ NULL) for
// an empty canonical.
func maskedMatchExcerpt(canonical string) string {
	return RedactExcerpt(canonical, canonical)
}

// mask returns a string of n maskRune glyphs.
func mask(n int) string { return strings.Repeat(string(maskRune), n) }

// maskAll replaces every regexp match with maskRune×rune-count, preserving the
// span length as 备案 context without revealing the value.
func maskAll(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		return mask(utf8.RuneCountInString(m))
	})
}

// clampWindow returns at most max runes of s, centred on the first masked span so
// the 命中 evidence is never truncated away. Rune-safe (never splits a rune) and
// panic-free for a match at either buffer edge (8.5-BLIND-BOUNDARY-001). Because
// masking runs over the FULL string before clamping, any clamped substring is
// already raw-content-free.
func clampWindow(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return string(runes)
	}
	center := 0
	for i, c := range runes {
		if c == maskRune {
			center = i
			break
		}
	}
	start := center - max/2
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(runes) {
		end = len(runes)
		start = end - max
	}
	return string(runes[start:end])
}
