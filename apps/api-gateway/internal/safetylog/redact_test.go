package safetylog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 8.5-UNIT-006 (HARD, property) — REDACTION-SAFETY. A context window with the
// matched span + surrounding email / phone / @handle PII must redact to: matched
// span = ■×rune-count, PII masked, ≤64 runes, the literal term NEVER stored, NO
// raw user text. BR-1.4 / Architect OQ-8.5-2.
func Test8_5_UNIT006_RedactionSafetyProperty(t *testing.T) {
	const term = "敏感词X"
	cases := []struct {
		name   string
		window string
		match  string
		// raw fragments that must NOT survive into the excerpt
		leaks []string
	}{
		{
			name:   "email around match",
			window: "please contact alice.smith@example.com about " + term + " now",
			match:  term,
			leaks:  []string{term, "alice.smith@example.com", "alice.smith"},
		},
		{
			name:   "phone digits around match",
			window: "call 138-1234-5678 re " + term,
			match:  term,
			leaks:  []string{term, "138-1234-5678", "12345678"},
		},
		{
			name:   "@handle around match",
			window: "ping @user_handle saw " + term + " here",
			match:  term,
			leaks:  []string{term, "@user_handle"},
		},
		{
			name:   "match at buffer start",
			window: term + " is the very first token of this window",
			match:  term,
			leaks:  []string{term},
		},
		{
			name:   "match at buffer end appears after a long lead in of filler text padding",
			window: "a very long lead in of filler text padding that exceeds the cap " + term,
			match:  term,
			leaks:  []string{term},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactExcerpt(tc.window, tc.match)
			if utf8.RuneCountInString(out) > maxExcerptRunes {
				t.Fatalf("excerpt %d runes exceeds cap %d: %q", utf8.RuneCountInString(out), maxExcerptRunes, out)
			}
			for _, leak := range tc.leaks {
				if strings.Contains(out, leak) {
					t.Fatalf("excerpt leaked %q: %q", leak, out)
				}
			}
			if !strings.ContainsRune(out, maskRune) {
				t.Fatalf("excerpt carries no mask glyph (no 命中 evidence): %q", out)
			}
		})
	}
}

// 8.5-BLIND-BOUNDARY-001 — excerpt window at exactly 64 runes; match at buffer
// ends; clamp never splits a rune nor panics.
func Test8_5_BLIND_BOUNDARY001_ExcerptCapAndEdges(t *testing.T) {
	long := strings.Repeat("x", 200) + "敏感" + strings.Repeat("y", 200)
	out := RedactExcerpt(long, "敏感")
	if got := utf8.RuneCountInString(out); got != maxExcerptRunes {
		t.Fatalf("expected clamp to %d runes, got %d", maxExcerptRunes, got)
	}
	if !strings.ContainsRune(out, maskRune) {
		t.Fatalf("masked span clamped away: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("clamp split a rune: %q", out)
	}
}

// 8.5-BLIND-BOUNDARY-002 — empty window → "" (recorder persists NULL, the
// conservative BR-1.4 fallback).
func Test8_5_BLIND_BOUNDARY002_EmptyWindowNull(t *testing.T) {
	if got := RedactExcerpt("", "anything"); got != "" {
		t.Fatalf("empty window must yield empty excerpt, got %q", got)
	}
}

// maskedMatchExcerpt (the v1 production path): canonical → ■×rune-count, never the
// canonical surface, ≤64 runes.
func Test8_5_MaskedMatchExcerpt(t *testing.T) {
	const canonical = "term_alpha"
	out := maskedMatchExcerpt(canonical)
	if strings.Contains(out, canonical) {
		t.Fatalf("excerpt contains the canonical surface: %q", out)
	}
	if want := strings.Repeat(string(maskRune), utf8.RuneCountInString(canonical)); out != want {
		t.Fatalf("excerpt = %q, want %q", out, want)
	}
	if maskedMatchExcerpt("") != "" {
		t.Fatalf("empty canonical must yield empty excerpt (→ NULL)")
	}
}
