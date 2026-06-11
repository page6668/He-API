// Story 8.4: 严格度配置（per Key）— the AC2 PURE-GATE core (implemented from the
// QA Test Design skeleton, Turing). HARD gates carried here (block merge — a 备案
// compliance breach if red):
//
//	G1 3×3 matrix .......... TestStrictness_BlocksMatrix
//	G2 first-qualifying-hit  TestScanTextMin_FirstQualifyingHit, _SubThresholdPassThrough
//	G3 fail-closed parse .... TestParseStrictness_FailClosed
//	G4 ScanText invariant ... TestScanTextMin_StrictEqualsPre84
//
// White-box (package contentsafety); reuses the existing lexiconWith / enRow /
// padRows helpers from scanner_test.go.
//
// Test Design: docs/qa/assessments/8.4-test-design-20260611.md
package contentsafety

import (
	"testing"

	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// gateLexicon builds a floor-satisfying lexicon seeded with one distinct term per
// severity so the gate + scan tests can drive (level × severity) without windowing
// surprises (ASCII, normalize-stable, unique vs padding).
func gateLexicon(t *testing.T) safetylexicon.Lexicon {
	t.Helper()
	return lexiconWith(t,
		enRow("lowsevword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 4001),
		enRow("medsevword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, 4002),
		enRow("highsevword", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 4003),
	)
}

// ============================================================
// AC2 (BR-2.1) — MinSeverity mapping  ·  8.4-UNIT-010
// ============================================================

func TestMinSeverity_RatifiedMapping(t *testing.T) {
	cases := []struct {
		level Strictness
		want  safetylexicon.Severity
	}{
		{Loose, safetylexicon.SeverityHigh},
		{Default, safetylexicon.SeverityMedium},
		{Strict, safetylexicon.SeverityLow},
	}
	for _, c := range cases {
		if got := MinSeverity(c.level); got != c.want {
			t.Fatalf("MinSeverity(%q) = %q, want %q", c.level, got, c.want)
		}
	}
}

// ============================================================
// AC2 (G1, BR-2.1) — Blocks 3×3 matrix  ·  8.4-UNIT-011
// ============================================================

func TestStrictness_BlocksMatrix(t *testing.T) {
	// rank: high(3) > medium(2) > low(1); Blocks := rank(sev) >= rank(MinSeverity(level)).
	type cell struct {
		level Strictness
		sev   safetylexicon.Severity
		want  bool
	}
	matrix := []cell{
		{Loose, safetylexicon.SeverityLow, false}, {Loose, safetylexicon.SeverityMedium, false}, {Loose, safetylexicon.SeverityHigh, true},
		{Default, safetylexicon.SeverityLow, false}, {Default, safetylexicon.SeverityMedium, true}, {Default, safetylexicon.SeverityHigh, true},
		{Strict, safetylexicon.SeverityLow, true}, {Strict, safetylexicon.SeverityMedium, true}, {Strict, safetylexicon.SeverityHigh, true},
	}
	for _, c := range matrix {
		if got := Blocks(c.sev, c.level); got != c.want {
			t.Fatalf("contentsafety: gate(level=%s, sev=%s) = %v want %v", c.level, c.sev, got, c.want)
		}
	}
}

func TestStrictness_BlocksBoundaryAtThreshold(t *testing.T) {
	// Inclusive (>=) threshold: a severity sitting exactly AT the level's minSeverity blocks.
	if !Blocks(safetylexicon.SeverityMedium, Default) {
		t.Fatal("medium under default must block (at-threshold inclusive)")
	}
	if Blocks(safetylexicon.SeverityMedium, Loose) {
		t.Fatal("medium under loose must NOT block (just below threshold)")
	}
	if !Blocks(safetylexicon.SeverityHigh, Loose) {
		t.Fatal("high under loose must block (just above threshold)")
	}
}

// ============================================================
// AC3 (G3, BR-3.4) — ParseStrictness fail-closed  ·  8.4-UNIT-035 / BLIND-BOUNDARY-002
// ============================================================

func TestParseStrictness_FailClosed(t *testing.T) {
	cases := []struct {
		in   string
		want Strictness
	}{
		{"strict", Strict},
		{"default", Default},
		{"loose", Loose},
		{"", Strict},        // pre-8.4 omitempty zero value
		{"off", Strict},     // unknown token
		{"high", Strict},    // severity token, NOT a level
		{"Strict", Strict},  // mixed-case never relaxes
		{"LOOSE", Strict},   // upper-case never relaxes
		{" loose ", Strict}, // whitespace-padded never relaxes
		{"default ", Strict},
	}
	for _, c := range cases {
		if got := ParseStrictness(c.in); got != c.want {
			t.Fatalf("8.4: relaxed gate on absent/invalid strictness — ParseStrictness(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// ============================================================
// AC2 (G2, BR-2.3) — ScanTextMin first-qualifying-hit  ·  8.4-UNIT-012/013/015
// ============================================================

func TestScanTextMin_FirstQualifyingHit(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	// LOW term appears BEFORE the HIGH term; loose (min=High) must skip the low
	// and return the HIGH match — never short-circuit on the sub-threshold low.
	text := "prefix lowsevword middle highsevword suffix"
	m, hit := s.ScanTextMin(text, safetylexicon.SeverityHigh)
	if !hit {
		t.Fatal("first-qualifying-hit: expected the HIGH term to be returned")
	}
	if m.Canonical != "highsevword" {
		t.Fatalf("8.4-UNIT-012: returned %q, want highsevword (low term must not mask the later high)", m.Canonical)
	}
}

func TestScanTextMin_SubThresholdPassThrough(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	text := "say lowsevword please" // only a LOW term
	// loose (min=High): NOT blocked (below threshold)...
	if _, hit := s.ScanTextMin(text, safetylexicon.SeverityHigh); hit {
		t.Fatal("8.4-UNIT-013: sub-threshold low term must NOT block under loose")
	}
	// ...but still DETECTED by ScanText (no-FN intact; gating is post-detection).
	if m, hit := s.ScanText(text); !hit || m.Canonical != "lowsevword" {
		t.Fatalf("8.4-UNIT-013: ScanText must still DETECT the low term (got hit=%v rule=%q)", hit, m.Canonical)
	}
}

func TestScanTextMin_Determinism(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	text := "noise lowsevword and highsevword mixed"
	m1, h1 := s.ScanTextMin(text, safetylexicon.SeverityMedium)
	for i := 0; i < 50; i++ {
		m2, h2 := s.ScanTextMin(text, safetylexicon.SeverityMedium)
		if h1 != h2 || m1 != m2 {
			t.Fatalf("non-deterministic ScanTextMin: (%v,%+v) vs (%v,%+v)", h1, m1, h2, m2)
		}
	}
}

// ============================================================
// AC2 (G4, BR-2.3) — ScanText invariant (strict == pre-8.4)  ·  8.4-UNIT-014
// ============================================================

func TestScanTextMin_StrictEqualsPre84(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	inputs := []string{
		"",
		"   ",
		"totally clean text",
		"has lowsevword only",
		"has medsevword only",
		"has highsevword only",
		"lowsevword then medsevword then highsevword",
		"prefix highsevword lowsevword suffix",
	}
	for _, in := range inputs {
		mA, hA := s.ScanText(in)
		mB, hB := s.ScanTextMin(in, safetylexicon.SeverityLow)
		if hA != hB || mA != mB {
			t.Fatalf("8.4-UNIT-014: ScanText(%q)=(%v,%+v) != ScanTextMin(_,Low)=(%v,%+v)", in, hA, mA, hB, mB)
		}
	}
}

// ============================================================
// AC2 (input path) — ScanMin multi-message variant  ·  8.4-UNIT-016
// ============================================================

func TestScanMin_FirstQualifyingAcrossMessages(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	contents := []string{"has lowsevword here", "has highsevword here"}
	// loose (min=High): the low in msg[0] must NOT short-circuit; returns the high.
	m, hit := s.ScanMin(contents, safetylexicon.SeverityHigh)
	if !hit || m.Canonical != "highsevword" {
		t.Fatalf("8.4-UNIT-016: ScanMin returned (%v,%q), want the high from msg[1]", hit, m.Canonical)
	}
	// Strict invariant for the input path: Scan == ScanMin(_, SeverityLow).
	mA, hA := s.Scan(contents)
	mB, hB := s.ScanMin(contents, safetylexicon.SeverityLow)
	if hA != hB || mA != mB {
		t.Fatalf("Scan != ScanMin(_,Low): (%v,%+v) vs (%v,%+v)", hA, mA, hB, mB)
	}
}

// ============================================================
// AC2 (BR-2.6) — empty / boundary inputs  ·  8.4-BLIND-BOUNDARY-003
// ============================================================

func TestScanTextMin_EmptyAndBoundary(t *testing.T) {
	s := NewScanner(gateLexicon(t))
	mins := []safetylexicon.Severity{safetylexicon.SeverityHigh, safetylexicon.SeverityMedium, safetylexicon.SeverityLow}
	for _, min := range mins {
		for _, in := range []string{"", "   \t\n  "} {
			if _, hit := s.ScanTextMin(in, min); hit {
				t.Fatalf("empty/whitespace input matched under min=%s", min)
			}
		}
		if _, hit := s.ScanMin(nil, min); hit {
			t.Fatalf("nil contents matched under min=%s", min)
		}
		if _, hit := s.ScanMin([]string{}, min); hit {
			t.Fatalf("empty contents matched under min=%s", min)
		}
	}
}
