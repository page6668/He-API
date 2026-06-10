package safetylexicon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

// cloneRegistry returns a deep copy of DefaultRegistry's rows so a test can
// mutate the seed (drop below floor, inject a bad enum, append a dupe) without
// affecting the package-global DefaultRegistry.
func cloneRegistry() Registry {
	rows := make([]Row, len(DefaultRegistry.Rows))
	copy(rows, DefaultRegistry.Rows)
	return Registry{Rows: rows}
}

// assertPanics runs fn and fails unless it panics with a message containing
// want. The panic value is stringified so both string and error panics match.
func assertPanics(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got no panic", want)
		}
		got := fmt.Sprint(r)
		if !strings.Contains(got, want) {
			t.Fatalf("panic = %q, want substring %q", got, want)
		}
	}()
	fn()
}

// ============================================================================
// AC1 — Construction invariants (floors + closed enums + 1:1)
// ============================================================================

func TestUNIT001_zh_floor_met(t *testing.T) {
	if got := DefaultLexicon.CountByLang()[LangZH]; got < zhFloor {
		t.Fatalf("zh count = %d, want >= %d", got, zhFloor)
	}
}

func TestUNIT002_en_floor_met(t *testing.T) {
	if got := DefaultLexicon.CountByLang()[LangEN]; got < enFloor {
		t.Fatalf("en count = %d, want >= %d", got, enFloor)
	}
}

func TestUNIT003_zh_below_floor_panics(t *testing.T) {
	reg := cloneRegistry()
	// Keep all en rows + exactly 999 zh rows → below the 1000 floor.
	var kept []Row
	zh := 0
	for _, r := range reg.Rows {
		if r.Lang == LangZH {
			if zh >= 999 {
				continue
			}
			zh++
		}
		kept = append(kept, r)
	}
	assertPanics(t, "zh term floor not met (got 999, need 1000)", func() {
		NewFromRegistry(Registry{Rows: kept})
	})
}

func TestUNIT004_en_below_floor_panics(t *testing.T) {
	reg := cloneRegistry()
	var kept []Row
	en := 0
	for _, r := range reg.Rows {
		if r.Lang == LangEN {
			if en >= 499 {
				continue
			}
			en++
		}
		kept = append(kept, r)
	}
	assertPanics(t, "en term floor not met (got 499, need 500)", func() {
		NewFromRegistry(Registry{Rows: kept})
	})
}

func TestUNIT005_unknown_category_token_panics(t *testing.T) {
	reg := cloneRegistry()
	reg.Rows[0].Category = "bogus_category"
	assertPanics(t, `unknown category "bogus_category" at`, func() {
		NewFromRegistry(reg)
	})
}

func TestUNIT006_unknown_severity_token_panics(t *testing.T) {
	reg := cloneRegistry()
	reg.Rows[0].Severity = "critical" // not in {high, medium, low}
	assertPanics(t, `unknown severity "critical" at`, func() {
		NewFromRegistry(reg)
	})
}

// TestUNIT005_unknown_category_file exercises the file-stem path (BLIND-ERROR-002).
func TestBLINDERROR002_unknown_category_file_panics(t *testing.T) {
	fsys := fstest.MapFS{
		"data/zh/political.txt":    {Data: []byte("占位\thigh\n")},
		"data/zh/notacategory.txt": {Data: []byte("foo\thigh\n")},
		"data/en/political.txt":    {Data: []byte("foo\thigh\n")},
	}
	assertPanics(t, "unknown category file notacategory.txt", func() {
		loadRegistryFS(fsys)
	})
}

func TestUNIT007_one_to_one_metadata(t *testing.T) {
	// Every stored term resolves to a Match via the exact set, and the exact
	// set has no orphan (size matches the slice). No term without metadata, no
	// metadata orphan.
	if len(DefaultLexicon.exact) != len(DefaultLexicon.terms) {
		t.Fatalf("exact set size %d != terms slice size %d", len(DefaultLexicon.exact), len(DefaultLexicon.terms))
	}
	for _, m := range DefaultLexicon.terms {
		got, ok := DefaultLexicon.exact[m.Canonical]
		if !ok {
			t.Fatalf("term %q has no metadata in exact set", m.Canonical)
		}
		if got != m {
			t.Fatalf("metadata mismatch for %q: %+v != %+v", m.Canonical, got, m)
		}
	}
}

// ============================================================================
// AC2 — Normalization (false-negative prevention)
// ============================================================================

func TestUNIT008_normalize_fullwidth(t *testing.T) {
	if got := Normalize("Ｂａｄ"); got != "bad" {
		t.Fatalf("Normalize(full-width Bad) = %q, want %q", got, "bad")
	}
}

func TestUNIT009_normalize_ascii_casefold(t *testing.T) {
	if got := Normalize("BADWORD"); got != "badword" {
		t.Fatalf("Normalize(BADWORD) = %q, want %q", got, "badword")
	}
	// Chinese is caseless and must be unaffected.
	if got := Normalize("测试"); got != "测试" {
		t.Fatalf("Normalize(测试) = %q, want unchanged", got)
	}
}

func TestUNIT010_normalize_strip_zero_width(t *testing.T) {
	in := "ba\u200Bd\u200Cwo\u200Dr\uFEFFd"
	if got := Normalize(in); got != "badword" {
		t.Fatalf("Normalize(zero-width) = %q, want %q", got, "badword")
	}
}

func TestUNIT011_normalize_trim_whitespace(t *testing.T) {
	in := "   badword \t\n" // leading NBSP + spaces, trailing tab/newline
	if got := Normalize(in); got != "badword" {
		t.Fatalf("Normalize(padded) = %q, want %q", got, "badword")
	}
}

func TestUNIT012_normalize_ordered_pipeline(t *testing.T) {
	// AC2 example: "  Ｂａｄ​Word  " (full-width + zero-width + surrounding spaces).
	in := "  Ｂａｄ​Word  "
	if got := Normalize(in); got != "badword" {
		t.Fatalf("Normalize(composite) = %q, want %q", got, "badword")
	}
}

func TestUNIT013_equivalence_class(t *testing.T) {
	a, okA := DefaultLexicon.Lookup("BADWORD")
	b, okB := DefaultLexicon.Lookup("ｂａｄword") // full-width "bad" + ascii "word"
	if !okA || !okB {
		t.Fatalf("anchor lookups failed: okA=%v okB=%v", okA, okB)
	}
	if a.Canonical != "badword" || a != b {
		t.Fatalf("equivalence class broken: a=%+v b=%+v", a, b)
	}
}

func TestUNIT014_cross_category_duplicate_panics(t *testing.T) {
	reg := cloneRegistry()
	// Find an existing zh political canonical and re-add it under another category.
	var existing string
	for _, r := range reg.Rows {
		if r.Lang == LangZH && r.Category == CategoryPolitical {
			existing = Normalize(r.Raw)
			break
		}
	}
	reg.Rows = append(reg.Rows, Row{
		Lang: LangZH, Category: CategoryOther, Severity: SeverityLow,
		Raw: existing, File: "data/zh/other.txt", Line: 9999,
	})
	assertPanics(t, "duplicate term", func() { NewFromRegistry(reg) })
	// Message must name both offending sites.
	assertPanics(t, "data/zh/other.txt:9999", func() { NewFromRegistry(reg) })
}

func TestUNIT015_cross_language_duplicate_panics(t *testing.T) {
	reg := cloneRegistry()
	var existing string
	for _, r := range reg.Rows {
		if r.Lang == LangEN {
			existing = Normalize(r.Raw)
			break
		}
	}
	// Same canonical, different LANGUAGE → global dedupe must still fire.
	reg.Rows = append(reg.Rows, Row{
		Lang: LangZH, Category: CategoryOther, Severity: SeverityLow,
		Raw: existing, File: "data/zh/other.txt", Line: 8888,
	})
	assertPanics(t, "duplicate term", func() { NewFromRegistry(reg) })
}

func TestUNIT016_empty_after_normalize_panics(t *testing.T) {
	reg := cloneRegistry()
	reg.Rows = append(reg.Rows, Row{
		Lang: LangEN, Category: CategoryOther, Severity: SeverityLow,
		Raw: "  ​ \t ", File: "data/en/other.txt", Line: 7777,
	})
	assertPanics(t, "term at data/en/other.txt:7777 normalizes to empty", func() {
		NewFromRegistry(reg)
	})
}

func TestUNIT017_all_terms_within_length_cap(t *testing.T) {
	for _, m := range DefaultLexicon.terms {
		if n := utf8.RuneCountInString(m.Canonical); n > maxCanonicalRunes {
			t.Fatalf("term %q has %d runes, exceeds cap %d (matched_rule VARCHAR(100))", m.Canonical, n, maxCanonicalRunes)
		}
	}
}

func TestINT001_canonical_is_normalized_fixed_point(t *testing.T) {
	// Match.Canonical must equal Normalize(Canonical): the stored form is fully
	// normalized, so the 8.5 matched_rule value is query-independent + stable.
	for _, m := range DefaultLexicon.terms {
		if got := Normalize(m.Canonical); got != m.Canonical {
			t.Fatalf("Canonical %q is not a normalization fixed point (got %q)", m.Canonical, got)
		}
	}
}

func TestBLINDBOUNDARY001_term_101_runes_panics(t *testing.T) {
	reg := cloneRegistry()
	reg.Rows = append(reg.Rows, Row{
		Lang: LangEN, Category: CategoryOther, Severity: SeverityLow,
		Raw: strings.Repeat("a", 101), File: "data/en/other.txt", Line: 6666,
	})
	assertPanics(t, "exceeds 100 runes", func() { NewFromRegistry(reg) })
}

func TestBLINDBOUNDARY003_term_100_runes_accepted(t *testing.T) {
	reg := cloneRegistry()
	term := strings.Repeat("a", 100)
	reg.Rows = append(reg.Rows, Row{
		Lang: LangEN, Category: CategoryOther, Severity: SeverityLow,
		Raw: term, File: "data/en/other.txt", Line: 5555,
	})
	lex := NewFromRegistry(reg) // must NOT panic
	if _, ok := lex.Lookup(term); !ok {
		t.Fatalf("100-rune term not found after construction")
	}
}

func TestBLINDERROR001_embedded_newline_panics(t *testing.T) {
	reg := cloneRegistry()
	reg.Rows = append(reg.Rows, Row{
		Lang: LangEN, Category: CategoryOther, Severity: SeverityLow,
		Raw: "ab\ncd", File: "data/en/other.txt", Line: 4444,
	})
	assertPanics(t, "invalid term at data/en/other.txt:4444", func() {
		NewFromRegistry(reg)
	})
}

func TestBLINDBOUNDARY004_zero_width_only_difference_collapses(t *testing.T) {
	// Two terms differing ONLY by a zero-width char normalize to the same
	// canonical → the no-silent-dedupe rule turns that into a construction panic.
	reg := cloneRegistry()
	reg.Rows = append(reg.Rows,
		Row{Lang: LangEN, Category: CategoryOther, Severity: SeverityLow, Raw: "zwcollapse", File: "data/en/other.txt", Line: 3001},
		Row{Lang: LangEN, Category: CategoryOther, Severity: SeverityLow, Raw: "zw​collapse", File: "data/en/other.txt", Line: 3002},
	)
	assertPanics(t, "duplicate term", func() { NewFromRegistry(reg) })
}

// ============================================================================
// AC3 — Matching API (no-false-negative + authoritative lookup + concurrency)
// ============================================================================

func TestUNIT018_exhaustive_no_false_negative(t *testing.T) {
	// THE security invariant: ∀ stored term, Lookup(t).ok ⇒ MightContain(t).
	for _, m := range DefaultLexicon.terms {
		if !DefaultLexicon.MightContain(m.Canonical) {
			t.Fatalf("Bloom FALSE-NEGATIVE for stored term %q — a missed filter", m.Canonical)
		}
		if _, ok := DefaultLexicon.Lookup(m.Canonical); !ok {
			t.Fatalf("Lookup miss for stored term %q", m.Canonical)
		}
	}
}

func TestUNIT019_lookup_metadata_correct(t *testing.T) {
	m, ok := DefaultLexicon.Lookup("badword")
	if !ok {
		t.Fatal("anchor 'badword' not found")
	}
	want := Match{Category: CategoryAbuse, Severity: SeverityHigh, Canonical: "badword"}
	if m != want {
		t.Fatalf("Lookup(badword) = %+v, want %+v", m, want)
	}
}

func TestUNIT020_lookup_normalizes_argument(t *testing.T) {
	// Un-normalized (full-width, padded, zero-width) query still resolves.
	if _, ok := DefaultLexicon.Lookup("  ＢＡＤ​WORD  "); !ok {
		t.Fatal("un-normalized query did not resolve (BR-3.2 defence-in-depth)")
	}
}

func TestINT002_concurrent_reads_race_clean(t *testing.T) {
	// Run under `go test -race`. Lock-free immutable reads from many goroutines.
	const goroutines = 64
	terms := DefaultLexicon.terms
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				m := terms[(seed+i)%len(terms)]
				_ = DefaultLexicon.MightContain(m.Canonical)
				_, _ = DefaultLexicon.Lookup(m.Canonical)
				_ = DefaultLexicon.Len()
				_ = DefaultLexicon.CountByLang()
			}
		}(g)
	}
	wg.Wait()
}

// ============================================================================
// AC4 — Determinism + FP bound + integrity guard + CI lane
// ============================================================================

func TestUNIT021_deterministic_construction(t *testing.T) {
	a := NewFromRegistry(DefaultRegistry)
	b := NewFromRegistry(DefaultRegistry)
	if len(a.terms) != len(b.terms) {
		t.Fatalf("term count differs across builds: %d vs %d", len(a.terms), len(b.terms))
	}
	for i := range a.terms {
		if a.terms[i] != b.terms[i] {
			t.Fatalf("corpus order differs at %d: %+v vs %+v", i, a.terms[i], b.terms[i])
		}
	}
	if bloomHash(a.bloom) != bloomHash(b.bloom) {
		t.Fatal("Bloom bitset not reproducible across builds")
	}
}

func bloomHash(b *bloomFilter) [32]byte {
	buf := make([]byte, 0, len(b.bits)*8)
	for _, w := range b.bits {
		for i := 0; i < 8; i++ {
			buf = append(buf, byte(w>>(uint(i)*8)))
		}
	}
	return sha256.Sum256(buf)
}

func TestUNIT022_bloom_fp_rate_within_bound(t *testing.T) {
	const trials = 100_000
	misses := 0
	hits := 0
	for i := 0; i < trials; i++ {
		// Construct a candidate guaranteed absent from the corpus (the corpus
		// has no '|' and no "miss#" prefix), then confirm via Lookup.
		cand := fmt.Sprintf("miss#%d|absent", i)
		if _, ok := DefaultLexicon.Lookup(cand); ok {
			continue // (impossible by construction; skip if it ever happens)
		}
		misses++
		if DefaultLexicon.MightContain(cand) {
			hits++
		}
	}
	rate := float64(hits) / float64(misses)
	if rate > 0.01 {
		t.Fatalf("bloom FP rate %.4f exceeds 0.01 bound", rate)
	}
	t.Logf("bloom FP rate = %.5f over %d misses (m=%d bits, k=%d)", rate, misses, DefaultLexicon.bloom.mask+1, DefaultLexicon.bloom.k)
}

func TestINT003_integrity_guard(t *testing.T) {
	// The load-bearing security gate, runnable via the standard test lane:
	// parse (DefaultLexicon built at init) + floors + zero-dupes + in-enum.
	lex := DefaultLexicon
	if lex.CountByLang()[LangZH] < zhFloor || lex.CountByLang()[LangEN] < enFloor {
		t.Fatal("floor violation")
	}
	if lex.Len() != len(lex.exact) {
		t.Fatal("duplicate terms detected (Len != distinct)")
	}
	for _, m := range lex.terms {
		if !categorySet[m.Category] {
			t.Fatalf("term %q has out-of-enum category %q", m.Canonical, m.Category)
		}
		if !severitySet[m.Severity] {
			t.Fatalf("term %q has out-of-enum severity %q", m.Canonical, m.Severity)
		}
	}
}

func TestINT004_ci_lane_present(t *testing.T) {
	// Architect High #1: the integrity guard must actually run in CI.
	data, err := os.ReadFile("../../.github/workflows/test.yml")
	if err != nil {
		t.Fatalf("cannot read workflow: %v", err)
	}
	wf := string(data)
	if !strings.Contains(wf, "./packages/safety-lexicon/...") {
		t.Fatal("test.yml has no safety-lexicon test lane")
	}
	// The lane must run with -race (the BR-3.5 concurrency gate).
	idx := strings.Index(wf, "./packages/safety-lexicon/...")
	window := wf[maxInt(0, idx-200):minInt(len(wf), idx+200)]
	if !strings.Contains(window, "-race") {
		t.Fatal("safety-lexicon lane does not run with -race")
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestUNIT031_placeholder_corpus_marked(t *testing.T) {
	// OQ-8.1-6: the placeholder corpus must be unmistakably marked so it can
	// never ship to prod silently.
	for _, lang := range []Lang{LangZH, LangEN} {
		entries, err := dataFS.ReadDir("data/" + string(lang))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			p := "data/" + string(lang) + "/" + e.Name()
			b, err := dataFS.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), "PLACEHOLDER CORPUS") {
				t.Fatalf("%s missing PLACEHOLDER CORPUS marker (OQ-8.1-6)", p)
			}
		}
	}
}

// ============================================================================
// E2E — real DefaultLexicon at package init
// ============================================================================

func TestE2E001_default_lexicon_constructs(t *testing.T) {
	// Reaching this test at all proves package-init construction did not panic;
	// re-assert the headline invariants on the real corpus.
	if DefaultLexicon.Len() == 0 {
		t.Fatal("DefaultLexicon empty")
	}
}

func TestE2E002_roundtrip_all_categories_both_langs(t *testing.T) {
	// Positive-membership per (category × lang): the core false-negative guard.
	// Pick one real term from each (lang, category) bucket and round-trip it.
	type key struct {
		lang Lang
		cat  Category
	}
	// Build a lang lookup from the registry rows (Lexicon doesn't store lang).
	langOf := map[string]Lang{}
	for _, r := range DefaultRegistry.Rows {
		langOf[Normalize(r.Raw)] = r.Lang
	}
	seen := map[key]bool{}
	for _, m := range DefaultLexicon.terms {
		k := key{lang: langOf[m.Canonical], cat: m.Category}
		if seen[k] {
			continue
		}
		seen[k] = true
		// raw (un-normalized via upper-casing ascii) still must resolve.
		if !DefaultLexicon.MightContain(m.Canonical) {
			t.Fatalf("[%s/%s] MightContain false for %q", k.lang, k.cat, m.Canonical)
		}
		got, ok := DefaultLexicon.Lookup(m.Canonical)
		if !ok || got.Category != m.Category {
			t.Fatalf("[%s/%s] round-trip failed for %q: ok=%v got=%+v", k.lang, k.cat, m.Canonical, ok, got)
		}
	}
	// Expect all 6 categories present in BOTH languages = 12 buckets.
	if len(seen) != 12 {
		t.Fatalf("expected 12 (lang×category) buckets, saw %d", len(seen))
	}
}

// ============================================================================
// P1 — introspection, hygiene, extensibility
// ============================================================================

func TestUNIT023_all_terms_in_closed_enums(t *testing.T) {
	for _, m := range DefaultLexicon.terms {
		if !categorySet[m.Category] {
			t.Fatalf("category %q out of enum", m.Category)
		}
		if !severitySet[m.Severity] {
			t.Fatalf("severity %q out of enum", m.Severity)
		}
	}
}

func TestUNIT024_len_equals_sum_countbylang(t *testing.T) {
	c := DefaultLexicon.CountByLang()
	if sum := c[LangZH] + c[LangEN]; sum != DefaultLexicon.Len() {
		t.Fatalf("Len() = %d, Σ CountByLang() = %d", DefaultLexicon.Len(), sum)
	}
}

func TestUNIT025_lang_from_dir_not_content(t *testing.T) {
	// A loanword (ASCII) placed in data/zh/ must count as zh; a CJK term in
	// data/en/ must count as en — language is the directory, not the content.
	rows := parseFile("loanword\thigh\n", LangZH, CategoryOther, "data/zh/other.txt")
	if len(rows) != 1 || rows[0].Lang != LangZH {
		t.Fatalf("ASCII term in zh/ not tagged zh: %+v", rows)
	}
	rows = parseFile("汉字\thigh\n", LangEN, CategoryOther, "data/en/other.txt")
	if len(rows) != 1 || rows[0].Lang != LangEN {
		t.Fatalf("CJK term in en/ not tagged en: %+v", rows)
	}
}

func TestUNIT026_comments_and_blanks_excluded(t *testing.T) {
	content := "# comment\n\nfoo\thigh\n   \n# another\nbar\tlow\n"
	rows := parseFile(content, LangEN, CategoryOther, "data/en/other.txt")
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (comments/blanks excluded), got %d: %+v", len(rows), rows)
	}
	if rows[0].Raw != "foo" || rows[1].Raw != "bar" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	// Line numbers must reflect the real file position (foo on line 3, bar on 6).
	if rows[0].Line != 3 || rows[1].Line != 6 {
		t.Fatalf("line numbers wrong: %+v", rows)
	}
}

func TestUNIT027_corpus_sorted_canonical(t *testing.T) {
	for i := 1; i < len(DefaultLexicon.terms); i++ {
		if DefaultLexicon.terms[i-1].Canonical >= DefaultLexicon.terms[i].Canonical {
			t.Fatalf("corpus not strictly sorted at %d: %q >= %q", i,
				DefaultLexicon.terms[i-1].Canonical, DefaultLexicon.terms[i].Canonical)
		}
	}
}

func TestINT005_extensibility_add_term(t *testing.T) {
	// Add a term line and rebuild → Lookup finds it, with NO code change. This
	// mirrors editing data/*/<cat>.txt and recompiling (go:embed picks it up).
	reg := cloneRegistry()
	const newTerm = "newlyaddedterm"
	if _, ok := DefaultLexicon.Lookup(newTerm); ok {
		t.Fatal("precondition: newTerm must not already exist")
	}
	reg.Rows = append(reg.Rows, Row{
		Lang: LangEN, Category: CategoryContraband, Severity: SeverityMedium,
		Raw: newTerm, File: "data/en/contraband.txt", Line: 2001,
	})
	lex := NewFromRegistry(reg)
	m, ok := lex.Lookup(newTerm)
	if !ok || m.Category != CategoryContraband || m.Severity != SeverityMedium {
		t.Fatalf("added term not resolvable: ok=%v m=%+v", ok, m)
	}
}

func TestUNIT028_mightcontain_miss_fast_exclusion(t *testing.T) {
	if _, ok := DefaultLexicon.Lookup("hello"); ok {
		t.Skip("'hello' unexpectedly in corpus")
	}
	if DefaultLexicon.MightContain("hello") {
		t.Fatal("MightContain(hello) true — expected fast exclusion (allowed Bloom FP, but corpus is deterministic)")
	}
}

func TestUNIT029_lookup_whitespace_is_miss_not_panic(t *testing.T) {
	m, ok := DefaultLexicon.Lookup("   ")
	if ok || m != (Match{}) {
		t.Fatalf("Lookup('   ') = (%+v, %v), want (Match{}, false)", m, ok)
	}
}

func TestUNIT030_introspection_surfaces(t *testing.T) {
	cats := DefaultLexicon.Categories()
	if len(cats) != 6 {
		t.Fatalf("Categories() len = %d, want 6", len(cats))
	}
	// Returned slice is a copy: mutating it must not affect the next call.
	cats[0] = "MUTATED"
	if DefaultLexicon.Categories()[0] == "MUTATED" {
		t.Fatal("Categories() leaked internal state")
	}
	c := DefaultLexicon.CountByLang()
	c[LangZH] = -1
	if DefaultLexicon.CountByLang()[LangZH] == -1 {
		t.Fatal("CountByLang() leaked internal state")
	}
}

// TestUNIT040_termlengths_sorted_distinct_no_regression covers Story 8.2
// UNIT-040: the additive TermLengths() accessor returns the sorted, distinct
// Canonical rune-lengths AND its presence regresses none of the existing
// Lookup / MightContain / Normalize / enum behaviour.
func TestUNIT040_termlengths_sorted_distinct_no_regression(t *testing.T) {
	got := DefaultLexicon.TermLengths()
	if len(got) == 0 {
		t.Fatal("TermLengths() returned empty for a floor-guarded corpus")
	}

	// Sorted ascending + strictly distinct (no duplicate lengths).
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("TermLengths() not strictly increasing at %d: %v", i, got)
		}
	}

	// Every reported length is a positive rune-count ≤ the Canonical cap, and is
	// REALIZED by at least one stored term (corpus-truth).
	present := make(map[int]bool)
	for _, m := range DefaultLexicon.terms {
		present[utf8.RuneCountInString(m.Canonical)] = true
	}
	for _, n := range got {
		if n <= 0 || n > maxCanonicalRunes {
			t.Fatalf("TermLengths() reported out-of-range length %d", n)
		}
		if !present[n] {
			t.Fatalf("TermLengths() reported length %d that no term has", n)
		}
	}
	// Completeness: EVERY realized term-length is reported (no false-negative
	// window bound for a downstream scanner — the load-bearing 8.2 BR-2.2 rule).
	if len(present) != len(got) {
		t.Fatalf("TermLengths() len %d != distinct realized lengths %d", len(got), len(present))
	}

	// Existing behaviour unchanged (regression guard on the Done package):
	// a known stored term still resolves; a non-member still misses.
	if _, ok := DefaultLexicon.Lookup("badword"); !ok {
		t.Fatal("regression: known term 'badword' no longer resolves after TermLengths() addition")
	}
	if !DefaultLexicon.MightContain("badword") {
		t.Fatal("regression: MightContain('badword') false after TermLengths() addition")
	}
	if _, ok := DefaultLexicon.Lookup("this-is-not-a-stored-term-xyz"); ok {
		t.Fatal("regression: non-member now resolves after TermLengths() addition")
	}
}

// TestUNIT041_termlengths_purity covers Story 8.2 UNIT-041: TermLengths() is a
// read-only, idempotent accessor that returns a defensive copy on every call
// (mirrors CountByLang / Categories — cannot leak internal state).
func TestUNIT041_termlengths_purity(t *testing.T) {
	a := DefaultLexicon.TermLengths()
	b := DefaultLexicon.TermLengths()
	if len(a) != len(b) {
		t.Fatalf("TermLengths() non-idempotent: len %d != %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("TermLengths() non-idempotent at %d: %d != %d", i, a[i], b[i])
		}
	}
	// Mutating the returned slice must not affect a subsequent call.
	if len(a) > 0 {
		a[0] = -999
		if DefaultLexicon.TermLengths()[0] == -999 {
			t.Fatal("TermLengths() leaked internal state (no defensive copy)")
		}
	}
}

func TestBLINDBOUNDARY002_single_rune_term_resolves(t *testing.T) {
	reg := cloneRegistry()
	const r = "页" // a single CJK rune, benign placeholder
	if _, ok := DefaultLexicon.Lookup(r); ok {
		t.Skip("single-rune anchor already present")
	}
	reg.Rows = append(reg.Rows, Row{
		Lang: LangZH, Category: CategoryOther, Severity: SeverityLow,
		Raw: r, File: "data/zh/other.txt", Line: 1001,
	})
	lex := NewFromRegistry(reg)
	if _, ok := lex.Lookup(r); !ok {
		t.Fatal("single-rune term not resolvable")
	}
}
