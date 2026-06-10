// Story 8.2 — contentsafety scanner tests (AC2 segmentation/no-false-negative +
// AC4 determinism + blind spots). White-box (package contentsafety) so the
// Bloom-before-Lookup ordering (UNIT-015) and the corpus-truth window bound
// (UNIT-014) can be asserted against the scanner's internals + a spy lexicon.
//
// Scenario IDs trace to docs/qa/assessments/8.2-test-design-20260610.md.
// HARD gates (block merge): 8.2-UNIT-013, 8.2-UNIT-014. No weakening.
package contentsafety

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// ----- test corpus helpers ----------------------------------------------
//
// NewFromRegistry enforces the 8.1 hard floors (zh ≥ 1000, en ≥ 500), so a
// controlled test lexicon is padded with benign distinct terms up to the floor
// and then seeded with the specific terms a scenario needs. The floors are
// mirrored here as test constants; over-padding is harmless (≥ floor is all
// NewFromRegistry requires).
const (
	testZHFloor = 1000
	testENFloor = 500
)

var padRows = buildPadRows()

func buildPadRows() []safetylexicon.Row {
	rows := make([]safetylexicon.Row, 0, testZHFloor+testENFloor)
	for i := 0; i < testZHFloor; i++ {
		rows = append(rows, safetylexicon.Row{
			Lang: safetylexicon.LangZH, Category: safetylexicon.CategoryOther,
			Severity: safetylexicon.SeverityLow,
			Raw:      fmt.Sprintf("占位填充%05d", i), File: "test/zh/other.txt", Line: i + 1,
		})
	}
	for i := 0; i < testENFloor; i++ {
		rows = append(rows, safetylexicon.Row{
			Lang: safetylexicon.LangEN, Category: safetylexicon.CategoryOther,
			Severity: safetylexicon.SeverityLow,
			Raw:      fmt.Sprintf("padfill-en-%05d", i), File: "test/en/other.txt", Line: i + 1,
		})
	}
	return rows
}

// lexiconWith builds a floor-satisfying test lexicon with the supplied extra
// terms appended to the benign padding.
func lexiconWith(t *testing.T, extra ...safetylexicon.Row) safetylexicon.Lexicon {
	t.Helper()
	rows := make([]safetylexicon.Row, 0, len(padRows)+len(extra))
	rows = append(rows, padRows...)
	rows = append(rows, extra...)
	return safetylexicon.NewFromRegistry(safetylexicon.Registry{Rows: rows})
}

func zhRow(raw string, cat safetylexicon.Category, sev safetylexicon.Severity, line int) safetylexicon.Row {
	return safetylexicon.Row{Lang: safetylexicon.LangZH, Category: cat, Severity: sev, Raw: raw, File: "test/zh/seed.txt", Line: line}
}

func enRow(raw string, cat safetylexicon.Category, sev safetylexicon.Severity, line int) safetylexicon.Row {
	return safetylexicon.Row{Lang: safetylexicon.LangEN, Category: cat, Severity: sev, Raw: raw, File: "test/en/seed.txt", Line: line}
}

// seedTerm pairs a stored term with its expected category for the exhaustive
// no-false-negative sweep (one per category × language).
type seedTerm struct {
	raw  string
	cat  safetylexicon.Category
	sev  safetylexicon.Severity
	lang safetylexicon.Lang
}

// sixByTwo is one distinct stored term per (category × language) — the corpus
// the HARD no-false-negative sweep (UNIT-013) exercises. Each term is
// normalize-stable (en lowercase ASCII / zh CJK) and unique vs the padding.
var sixByTwo = []seedTerm{
	{"敏政词甲", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, safetylexicon.LangZH},
	{"敏色词乙", safetylexicon.CategoryPornographic, safetylexicon.SeverityHigh, safetylexicon.LangZH},
	{"敏暴词丙", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, safetylexicon.LangZH},
	{"敏禁词丁", safetylexicon.CategoryContraband, safetylexicon.SeverityMedium, safetylexicon.LangZH},
	{"敏辱词戊", safetylexicon.CategoryAbuse, safetylexicon.SeverityLow, safetylexicon.LangZH},
	{"敏其词己", safetylexicon.CategoryOther, safetylexicon.SeverityLow, safetylexicon.LangZH},
	{"polsensitiveword", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, safetylexicon.LangEN},
	{"pornsensitiveword", safetylexicon.CategoryPornographic, safetylexicon.SeverityHigh, safetylexicon.LangEN},
	{"violsensitiveword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, safetylexicon.LangEN},
	{"contrasensitiveword", safetylexicon.CategoryContraband, safetylexicon.SeverityMedium, safetylexicon.LangEN},
	{"abusesensitiveword", safetylexicon.CategoryAbuse, safetylexicon.SeverityLow, safetylexicon.LangEN},
	{"othersensitiveword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, safetylexicon.LangEN},
}

func sixByTwoLexicon(t *testing.T) safetylexicon.Lexicon {
	t.Helper()
	extra := make([]safetylexicon.Row, 0, len(sixByTwo))
	for i, s := range sixByTwo {
		if s.lang == safetylexicon.LangZH {
			extra = append(extra, zhRow(s.raw, s.cat, s.sev, 2000+i))
		} else {
			extra = append(extra, enRow(s.raw, s.cat, s.sev, 2000+i))
		}
	}
	return lexiconWith(t, extra...)
}

// ----- AC2: segmentation + Bloom + NO FALSE-NEGATIVE --------------------

// 8.2-UNIT-010 — an embedded CJK term (no word boundary) is found by windowing.
func TestScan_UNIT010_EmbeddedCJKTerm(t *testing.T) {
	lex := lexiconWith(t, zhRow("敏政词甲", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 3001))
	s := NewScanner(lex)
	m, hit := s.ScanText("前缀无关文字敏政词甲后缀更多文字")
	if !hit {
		t.Fatal("embedded CJK term not detected via windowing")
	}
	if m.Canonical != "敏政词甲" {
		t.Fatalf("matched_rule = %q, want 敏政词甲", m.Canonical)
	}
}

// 8.2-UNIT-011 — an ASCII term embedded in surrounding text is detected.
// (Tokenizing on punctuation would shatter hyphenated corpus terms — see the
// ScanText doc rationale — so the scanner windows ASCII uniformly too.)
func TestScan_UNIT011_ASCIITokenMatch(t *testing.T) {
	lex := lexiconWith(t, enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3002))
	s := NewScanner(lex)
	if _, hit := s.ScanText("please do not say badword in chat"); !hit {
		t.Fatal("space-delimited ASCII term not detected")
	}
	// A hyphenated term (the real corpus shape) must also be found — punctuation
	// is NOT a split boundary that could drop the candidate.
	lex2 := lexiconWith(t, enRow("placeholder-violence-0100", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityHigh, 3003))
	s2 := NewScanner(lex2)
	if _, hit := s2.ScanText("noise placeholder-violence-0100 noise"); !hit {
		t.Fatal("hyphenated ASCII term shattered by tokenization (false-negative)")
	}
}

// 8.2-UNIT-012 — width + case + zero-width evasion folds to the canonical via the
// shared Normalize and is detected.
func TestScan_UNIT012_NormalizationEvasion(t *testing.T) {
	lex := lexiconWith(t, enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3004))
	s := NewScanner(lex)
	// Ｂａｄ (full-width) + U+200C zero-width + "Word" (mixed case) → "badword".
	evasion := "prefix Ｂａｄ‌Word suffix"
	m, hit := s.ScanText(evasion)
	if !hit {
		t.Fatalf("normalization-evasion variant not detected: %q", evasion)
	}
	if m.Canonical != "badword" {
		t.Fatalf("matched_rule = %q, want badword", m.Canonical)
	}
}

// 8.2-UNIT-013 — HARD GATE: exhaustive no-false-negative across all 6 categories
// × both languages, each term embedded mid-text. A single miss is a 备案
// compliance breach. MUST NOT be weakened.
func TestScan_UNIT013_HARD_ExhaustiveNoFalseNegative(t *testing.T) {
	lex := sixByTwoLexicon(t)
	s := NewScanner(lex)
	for _, seed := range sixByTwo {
		// Embed mid-text with both CJK and ASCII surrounds (no boundary help).
		for _, tmpl := range []string{"前缀%s后缀", "noise %s noise", "%s", "...%s!!!"} {
			content := fmt.Sprintf(tmpl, seed.raw)
			m, hit := s.ScanText(content)
			if !hit {
				t.Fatalf("FALSE-NEGATIVE (compliance breach): stored term %q (cat=%s lang=%s) not detected in %q",
					seed.raw, seed.cat, seed.lang, content)
			}
			if m.Canonical != safetylexicon.Normalize(seed.raw) {
				t.Fatalf("term %q reported wrong canonical %q", seed.raw, m.Canonical)
			}
			if m.Category != seed.cat {
				t.Fatalf("term %q reported category %s, want %s", seed.raw, m.Category, seed.cat)
			}
		}
	}
}

// 8.2-UNIT-014 / 8.2-BLIND-BOUNDARY-004 — HARD GATE: the window bound is the
// corpus truth, not a magic constant. The longest stored term — at the exact
// max(TermLengths()) corpus window bound, longer than every other realized
// length — embedded mid-text MUST be detected, proving the scanner windows up to
// max(TermLengths()). MUST NOT be weakened.
func TestScan_UNIT014_HARD_CorpusTruthWindowBound(t *testing.T) {
	longTerm := strings.Repeat("z", 40) // 40 runes — longer than padding (≤16) + any seed
	lex := lexiconWith(t, enRow(longTerm, safetylexicon.CategoryContraband, safetylexicon.SeverityHigh, 3005))
	s := NewScanner(lex)

	// The scanner's largest window equals the corpus's longest term (descending).
	if got := s.descLengths[0]; got != utf8.RuneCountInString(longTerm) {
		t.Fatalf("max window = %d, want corpus-truth %d (magic-constant bound would miss the longest term)",
			got, utf8.RuneCountInString(longTerm))
	}
	m, hit := s.ScanText("前缀" + longTerm + "后缀")
	if !hit {
		t.Fatal("FALSE-NEGATIVE: longest stored term missed — window bound is not corpus-truth")
	}
	if m.Canonical != longTerm {
		t.Fatalf("matched_rule = %q, want the 40-rune term", m.Canonical)
	}
}

// spyLex wraps a real Lexicon and counts the membership calls so the test can
// assert Lookup is gated by a Bloom positive (the §9.3 layer-1 → layer-2 order).
type spyLex struct {
	inner     safetylexicon.Lexicon
	might     int
	mightTrue int
	look      int
}

func (s *spyLex) MightContain(term string) bool {
	s.might++
	r := s.inner.MightContain(term)
	if r {
		s.mightTrue++
	}
	return r
}

func (s *spyLex) Lookup(term string) (safetylexicon.Match, bool) {
	s.look++
	return s.inner.Lookup(term)
}

func (s *spyLex) TermLengths() []int { return s.inner.TermLengths() }

// 8.2-UNIT-015 — the Bloom prefilter runs per-candidate BEFORE the authoritative
// Lookup: every Lookup is preceded by a Bloom positive, and on clean text the
// Bloom excludes the vast majority of windows without any map hit.
func TestScan_UNIT015_BloomPrefilterBeforeLookup(t *testing.T) {
	spy := &spyLex{inner: lexiconWith(t, enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3006))}
	s := newScannerFromLexicon(spy)

	// Clean text: many windows tested, Lookup only ever on a Bloom positive.
	_, hit := s.ScanText("the quick brown fox jumps over the lazy dog")
	if hit {
		t.Fatal("clean text unexpectedly matched")
	}
	if spy.might == 0 {
		t.Fatal("Bloom prefilter never ran (no candidates tested)")
	}
	if spy.look > spy.mightTrue {
		t.Fatalf("Lookup (%d) called without a Bloom positive (%d) — layer ordering violated", spy.look, spy.mightTrue)
	}
	if spy.look >= spy.might {
		t.Fatalf("Bloom excluded nothing: look=%d might=%d (prefilter not effective)", spy.look, spy.might)
	}

	// Hit text: the Bloom must pass at least one positive and Lookup confirm it.
	spy2 := &spyLex{inner: spy.inner}
	s2 := newScannerFromLexicon(spy2)
	if _, hit := s2.ScanText("say badword now"); !hit {
		t.Fatal("hit text not detected")
	}
	if spy2.mightTrue == 0 || spy2.look == 0 {
		t.Fatalf("hit path skipped a layer: mightTrue=%d look=%d", spy2.mightTrue, spy2.look)
	}
}

// 8.2-UNIT-016 — the consumer uses the SHARED safetylexicon.Normalize (not a
// re-implementation): a zero-width-injected variant of a stored term still folds
// to the canonical and is detected, identical to what Lookup would do directly.
func TestScan_UNIT016_SharedNormalize(t *testing.T) {
	lex := lexiconWith(t, enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3007))
	s := NewScanner(lex)
	withZW := "bad​word" // zero-width space inside the term
	// Direct Lookup folds it (shared Normalize); the scanner must reach the same.
	if _, ok := lex.Lookup(withZW); !ok {
		t.Fatal("precondition: Lookup should fold the zero-width variant")
	}
	if _, hit := s.ScanText("noise " + withZW + " noise"); !hit {
		t.Fatal("scanner did not fold zero-width variant — normalization drift (re-impl?)")
	}
}

// 8.2-UNIT-017 — longest-match-first: with both "ab" and "abc" stored, content
// "abc" reports the canonical("abc"), pinning the matched_rule for the 8.5 event
// under overlapping terms (Architect Low #1).
func TestScan_UNIT017_LongestMatchFirst(t *testing.T) {
	lex := lexiconWith(t,
		enRow("ab", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 3008),
		enRow("abc", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 3009),
	)
	s := NewScanner(lex)
	m, hit := s.ScanText("abc")
	if !hit {
		t.Fatal("overlapping-term content not matched")
	}
	if m.Canonical != "abc" {
		t.Fatalf("matched_rule = %q, want abc (longest-match-first)", m.Canonical)
	}
}

// 8.2-UNIT-018 — there is NO canonical-input fast path (Architect Medium): the
// only resolution path is canonicalized membership, so no fast path can trade
// re-normalization cost for a missed term. Re-assert no-false-negative holds for
// the (single) path on every seed term.
func TestScan_UNIT018_NoFastPathPreservesNoFalseNegative(t *testing.T) {
	lex := sixByTwoLexicon(t)
	s := NewScanner(lex)
	for _, seed := range sixByTwo {
		if _, hit := s.ScanText("xx" + seed.raw + "yy"); !hit {
			t.Fatalf("no-fast-path invariant violated: %q missed", seed.raw)
		}
	}
}

// ----- AC4: determinism -------------------------------------------------

// 8.2-UNIT-030 — deterministic verdict + matched_rule on repeated scans of the
// same input (pure scan over the immutable lexicon; no map-order dependence).
func TestScan_UNIT030_Determinism(t *testing.T) {
	lex := sixByTwoLexicon(t)
	s := NewScanner(lex)
	content := "前缀polsensitiveword和敏暴词丙混合"
	m1, h1 := s.ScanText(content)
	for i := 0; i < 50; i++ {
		m2, h2 := s.ScanText(content)
		if h1 != h2 || m1 != m2 {
			t.Fatalf("non-deterministic verdict: (%v,%+v) vs (%v,%+v)", h1, m1, h2, m2)
		}
	}
}

// ----- Blind spots: BOUNDARY / ERROR / CONCURRENCY / FLOW ---------------

// 8.2-BLIND-BOUNDARY-001 — empty content is clean, never an error.
func TestScan_BLIND_BOUNDARY001_EmptyContent(t *testing.T) {
	s := NewScanner(sixByTwoLexicon(t))
	if _, hit := s.ScanText(""); hit {
		t.Fatal("empty content must not match")
	}
}

// 8.2-BLIND-BOUNDARY-002 — whitespace-only content is clean (Normalize trims).
func TestScan_BLIND_BOUNDARY002_WhitespaceOnly(t *testing.T) {
	s := NewScanner(sixByTwoLexicon(t))
	if _, hit := s.ScanText("   \t\n  "); hit {
		t.Fatal("whitespace-only content must not match")
	}
}

// 8.2-BLIND-BOUNDARY-003 — empty messages slice → no candidate, proceeds clean.
func TestScan_BLIND_BOUNDARY003_EmptyMessages(t *testing.T) {
	s := NewScanner(sixByTwoLexicon(t))
	if _, hit := s.Scan(nil); hit {
		t.Fatal("nil contents must not match")
	}
	if _, hit := s.Scan([]string{}); hit {
		t.Fatal("empty contents must not match")
	}
}

// 8.2-BLIND-BOUNDARY-005 — a term that is the WHOLE content (no surround) is
// detected.
func TestScan_BLIND_BOUNDARY005_BareTerm(t *testing.T) {
	lex := lexiconWith(t, zhRow("敏政词甲", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 3010))
	s := NewScanner(lex)
	if _, hit := s.ScanText("敏政词甲"); !hit {
		t.Fatal("bare-term content not detected")
	}
}

// 8.2-BLIND-BOUNDARY-006 — a large body is scanned to completion (the real input
// is bounded at 1 MiB by the handler's MaxBytesReader, BR-4.4).
func TestScan_BLIND_BOUNDARY006_LargeBodyCompletes(t *testing.T) {
	lex := lexiconWith(t, enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3011))
	s := NewScanner(lex)
	big := strings.Repeat("啊", 200_000) // ~600 KB of benign CJK
	if _, hit := s.ScanText(big); hit {
		t.Fatal("benign large body unexpectedly matched")
	}
	// And a hit at the very end of a large body is still found.
	if _, hit := s.ScanText(big + " badword"); !hit {
		t.Fatal("term at end of large body missed")
	}
}

// 8.2-BLIND-BOUNDARY-007 / 8.2-BLIND-ERROR-001 — malformed / partial UTF-8 and
// other pathological input never panic; there is no runtime-failure branch.
func TestScan_BLIND_BOUNDARY007_ERROR001_NoPanicOnPathologicalInput(t *testing.T) {
	s := NewScanner(sixByTwoLexicon(t))
	inputs := []string{
		"\xff\xfe\xfa",             // invalid UTF-8 bytes
		"\x00\x00",                 // NULs
		"a\xffb敏政词甲c",              // invalid byte adjacent to a real term
		string([]byte{0xED, 0xA0}), // partial surrogate-ish bytes
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ScanText panicked on %q: %v", in, r)
				}
			}()
			_, _ = s.ScanText(in) // result irrelevant; MUST NOT panic
		}()
	}
}

// 8.2-BLIND-CONCURRENCY-001 (race-free) + 8.2-BLIND-CONCURRENCY-002 (deterministic
// verdict, no map-iteration-order dependence) — concurrent ScanText over the
// immutable lexicon is race-free (run under -race) and every goroutine returns
// the identical verdict for the same input.
func TestScan_BLIND_CONCURRENCY_RaceFreeDeterministic(t *testing.T) {
	lex := sixByTwoLexicon(t)
	s := NewScanner(lex)
	content := "前缀polsensitiveword后缀"
	want, wantHit := s.ScanText(content)

	const goroutines = 64
	var wg sync.WaitGroup
	errs := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				m, hit := s.ScanText(content)
				if hit != wantHit || m != want {
					errs <- fmt.Sprintf("concurrent verdict drift: got (%v,%+v) want (%v,%+v)", hit, m, wantHit, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// 8.2-BLIND-FLOW-002 — Scan over multiple contents returns the FIRST confirmed
// hit in array order (fail-fast reject), deterministically.
func TestScan_BLIND_FLOW002_FirstHitInArrayOrder(t *testing.T) {
	lex := lexiconWith(t,
		enRow("badword", safetylexicon.CategoryAbuse, safetylexicon.SeverityHigh, 3012),
		zhRow("敏政词甲", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 3013),
	)
	s := NewScanner(lex)
	m, hit := s.Scan([]string{"clean one", "has badword here", "敏政词甲也在这里"})
	if !hit {
		t.Fatal("expected a hit across the message array")
	}
	if m.Canonical != "badword" {
		t.Fatalf("first hit = %q, want badword (array order)", m.Canonical)
	}
}
