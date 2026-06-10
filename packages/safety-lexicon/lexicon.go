// Package safetylexicon is the single source of truth for the He-API
// content-safety sensitive-word lexicon: a version-controlled, extensible
// Chinese + English wordlist exposed as a pure, fast-lookup Go library with
// per-term {category, severity, canonical} metadata.
//
// Story 8.1 (Architect Round-1, Wright, RATIFIED): the corpus is a
// version-controlled CODE catalogue embedded via //go:embed — mirroring
// packages/plan-catalogue and packages/models-catalogue — NOT a runtime
// `safety_lexicon` PG table. The rationale carries over verbatim: review +
// no per-pod drift + 境内-safe (the corpus is compiled into the binary, never
// fetched over the network). Construction-time invariant enforcement (count
// floors, closed enums, no duplicates) is done via panic-at-construction: a
// drift here is a build-time programming error, not a runtime condition, and
// the AC4 CI integrity guard catches it before merge.
//
// The package carries PURE DOMAIN types only (no JSON/proto/HTTP concerns —
// same posture as models-catalogue / plan-catalogue). It owns the §9.3
// content-safety pipeline layers (1) Bloom quick-exclude and (2) dictionary
// keyword match; the model-classifier layer (3) and the actual request/response
// filter wiring are Stories 8.2 / 8.3 / 8.4 / 8.5 and are NOT in this story.
//
// Consumer contract: Stories 8.2 (入参) / 8.3 (出参) MUST normalize query text
// through the shared Normalize entrypoint before MightContain / Lookup so the
// producer and consumer can never drift (BR-2.2). Match.Canonical is the stable
// matched_rule value Story 8.5 persists to content_safety_logs.matched_rule
// (VARCHAR(100)); per-term Canonical length is therefore capped at 100 runes
// (Architect High #2).
package safetylexicon

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Category is a closed enum: the §9.3 governance taxonomy (OQ-8.1-5, ratified).
// An unknown category token in the data is a build-time programming error
// (panic at construction, BR-1.2).
type Category string

const (
	CategoryPolitical      Category = "political"
	CategoryPornographic   Category = "pornographic"
	CategoryViolenceTerror Category = "violence_terror"
	CategoryContraband     Category = "contraband"
	CategoryAbuse          Category = "abuse"
	CategoryOther          Category = "other"
)

// closedCategories is the canonical-ordered closed set; iteration order is
// fixed so Categories() and any derived structure is deterministic (BR-4.1).
var closedCategories = []Category{
	CategoryPolitical,
	CategoryPornographic,
	CategoryViolenceTerror,
	CategoryContraband,
	CategoryAbuse,
	CategoryOther,
}

var categorySet = func() map[Category]bool {
	m := make(map[Category]bool, len(closedCategories))
	for _, c := range closedCategories {
		m[c] = true
	}
	return m
}()

// Severity is a closed enum {high, medium, low}. It is the field Story 8.4 maps
// onto per-Key strictness (loose / default / strict); 8.1 only guarantees every
// term is severity-tagged (BR-1.3 / BR-1.4). An unknown severity token in the
// data panics at construction.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

var severitySet = map[Severity]bool{
	SeverityHigh:   true,
	SeverityMedium: true,
	SeverityLow:    true,
}

// Lang is derived deterministically from the source data file's language
// directory, NOT auto-detected from term content (BR-1.5) — this keeps the
// floor count auditable and avoids misclassifying loanwords.
type Lang string

const (
	LangZH Lang = "zh"
	LangEN Lang = "en"
)

const (
	// zhFloor / enFloor are HARD coverage floors (BR-1.1). A build below either
	// is a programming error → panic at construction (the AC4 CI guard catches
	// it pre-merge).
	zhFloor = 1000
	enFloor = 500

	// maxCanonicalRunes caps a stored term's Canonical length. It is aligned to
	// the downstream content_safety_logs.matched_rule VARCHAR(100) column
	// (Architect High #2 / BR-3.3) so the 8.5 insert can never overflow. 100
	// runes vastly exceeds any real sensitive term.
	maxCanonicalRunes = 100
)

// Match is the resolved metadata for a matched term. Canonical is the normalized
// stored form (NOT the raw query), so it is the query-independent matched_rule
// value Story 8.5 persists (BR-3.3).
type Match struct {
	Category  Category // §9.3 classification, feeds content_safety_logs (8.5)
	Severity  Severity // feeds per-Key strictness mapping (8.4)
	Canonical string   // normalized stored term == the stable matched_rule value
}

// Lexicon is an immutable, concurrent-safe view over the resolved corpus. After
// construction nothing is mutated, so all read methods are lock-free and safe
// for concurrent use from any number of goroutines (BR-3.5).
type Lexicon struct {
	exact  map[string]Match // canonical → Match (authoritative membership)
	terms  []Match          // sorted by Canonical (deterministic; BR-4.1)
	bloom  *bloomFilter     // §9.3 layer-1 prefilter, derived from terms
	counts map[Lang]int     // per-language term counts (floor surface)
}

// Normalize is the canonical, shared normalization entrypoint (BR-2.2). The
// SAME function MUST be applied by consumers (8.2/8.3) to query inputs before
// Lookup so the matching rule cannot drift between producer and consumer.
//
// The pipeline is fixed and ordered: NFKC compatibility fold → ASCII case-fold
// (Chinese is caseless and unaffected) → trim surrounding ASCII+Unicode
// whitespace → strip zero-width characters (U+200B/200C/200D/FEFF). Each step
// closes a known evasion class (width, case, padding, zero-width insertion);
// a bug here would propagate to every consumer as a false-negative, so this is
// a security-critical function.
//
// Out of scope (Architect OQ-8.1-4): simplified⇄traditional Chinese folding is
// DEFERRED to 8.2/8.3; leetspeak / pinyin / homoglyph evasion is the layer-3
// model classifier's job, not this deterministic dictionary layer.
func Normalize(s string) string {
	s = norm.NFKC.String(s)
	s = asciiCaseFold(s)
	s = strings.TrimFunc(s, unicode.IsSpace)
	s = stripZeroWidth(s)
	return s
}

// asciiCaseFold lowercases ASCII A–Z only; all non-ASCII runes (incl. Chinese,
// which is caseless) pass through unchanged. We deliberately do NOT use
// strings.ToLower, whose locale-sensitive special-casing (e.g. Turkish İ) is an
// undesirable, non-deterministic surface for a dictionary fold.
func asciiCaseFold(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// stripZeroWidth removes the zero-width characters commonly used to break up a
// term and evade an exact-match filter.
func stripZeroWidth(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\uFEFF':
			return -1
		}
		return r
	}, s)
}

// NewFromRegistry materialises a Lexicon from a Registry, enforcing every
// construction-time invariant via panic — it never returns a partially-loaded
// lexicon, because a drift here is a build-time programming error (the
// plan-catalogue / models-catalogue precedent):
//
//   - an unknown category token            (BR-1.2, UNIT-005);
//   - an unknown severity token            (BR-1.3, UNIT-006);
//   - a term with an embedded newline      (AC2 data-validation, BLIND-ERROR-001);
//   - a term that normalizes to empty      (BR-2, UNIT-016);
//   - a term whose Canonical > 100 runes   (Architect High #2, UNIT-017);
//   - a duplicate normalized term across ANY category/language
//     (BR-2.3, no silent dedupe — UNIT-014/015);
//   - a per-language count below the hard floor (BR-1.1, UNIT-003/004).
//
// The resolved corpus is sorted by Canonical before storage AND before the
// Bloom filter is populated, so the corpus order and the Bloom bit-set are
// byte-reproducible across runs/platforms (BR-4.1, determinism).
func NewFromRegistry(reg Registry) Lexicon {
	exact := make(map[string]Match, len(reg.Rows))
	origin := make(map[string]string, len(reg.Rows)) // canonical → "file:line" of first sighting
	counts := map[Lang]int{}
	terms := make([]Match, 0, len(reg.Rows))

	for _, row := range reg.Rows {
		if !categorySet[row.Category] {
			panic(fmt.Sprintf("safetylexicon: unknown category %q at %s:%d", row.Category, row.File, row.Line))
		}
		if !severitySet[row.Severity] {
			panic(fmt.Sprintf("safetylexicon: unknown severity %q at %s:%d", row.Severity, row.File, row.Line))
		}
		if strings.ContainsRune(row.Raw, '\n') {
			panic(fmt.Sprintf("safetylexicon: invalid term at %s:%d", row.File, row.Line))
		}

		canon := Normalize(row.Raw)
		if canon == "" {
			panic(fmt.Sprintf("safetylexicon: term at %s:%d normalizes to empty", row.File, row.Line))
		}
		if utf8.RuneCountInString(canon) > maxCanonicalRunes {
			panic(fmt.Sprintf("safetylexicon: term %q at %s:%d exceeds %d runes", canon, row.File, row.Line, maxCanonicalRunes))
		}

		if prev, dup := origin[canon]; dup {
			panic(fmt.Sprintf("safetylexicon: duplicate term %q (%s & %s:%d)", canon, prev, row.File, row.Line))
		}
		origin[canon] = fmt.Sprintf("%s:%d", row.File, row.Line)

		m := Match{Category: row.Category, Severity: row.Severity, Canonical: canon}
		exact[canon] = m
		terms = append(terms, m)
		counts[row.Lang]++
	}

	if counts[LangZH] < zhFloor {
		panic(fmt.Sprintf("safetylexicon: zh term floor not met (got %d, need %d)", counts[LangZH], zhFloor))
	}
	if counts[LangEN] < enFloor {
		panic(fmt.Sprintf("safetylexicon: en term floor not met (got %d, need %d)", counts[LangEN], enFloor))
	}

	// Determinism prerequisite (BR-4.1): canonical byte-order sort BEFORE the
	// Bloom is populated, so no map-iteration order can leak into the output.
	sort.Slice(terms, func(i, j int) bool { return terms[i].Canonical < terms[j].Canonical })

	bloom := newBloomFilter(len(terms))
	for _, m := range terms {
		bloom.add(m.Canonical)
	}

	return Lexicon{exact: exact, terms: terms, bloom: bloom, counts: counts}
}

// MightContain is the §9.3 layer-1 Bloom fast-path (98%+ non-hit exclusion). It
// MAY return a false-positive but MUST NEVER return a false-negative: the
// contract is Lookup(t).ok == true ⇒ MightContain(t) == true (BR-3.1). A
// MightContain==true / Lookup==false outcome is an allowed Bloom false-positive
// the consumer confirms with Lookup. The argument is normalized so the no-FN
// guarantee holds for un-normalized queries too.
func (l Lexicon) MightContain(term string) bool {
	return l.bloom.test(Normalize(term))
}

// Lookup is the authoritative exact-membership check. It normalizes its argument
// (defence-in-depth, BR-3.2) and returns the Match for the matched_rule Story
// 8.5 logs. An empty/whitespace-only or non-member query returns (Match{}, false)
// — never an error, never a panic (BR-3.5).
func (l Lexicon) Lookup(term string) (Match, bool) {
	canon := Normalize(term)
	if canon == "" {
		return Match{}, false
	}
	m, ok := l.exact[canon]
	return m, ok
}

// Len reports the total number of distinct terms in the lexicon.
func (l Lexicon) Len() int { return len(l.terms) }

// CountByLang reports the per-language term counts (the floor surface). The
// result is a fresh copy on every call so callers cannot mutate internal state.
func (l Lexicon) CountByLang() map[Lang]int {
	out := make(map[Lang]int, len(l.counts))
	for k, v := range l.counts {
		out[k] = v
	}
	return out
}

// Categories returns the closed governance taxonomy in canonical order. The
// result is a fresh copy on every call.
func (l Lexicon) Categories() []Category {
	out := make([]Category, len(closedCategories))
	copy(out, closedCategories)
	return out
}
