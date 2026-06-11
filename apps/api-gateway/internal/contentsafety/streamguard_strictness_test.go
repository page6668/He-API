// Story 8.4 — StreamGuard severity gating (NewStreamGuardMin): 8.4-UNIT-019/020
// (G2 stream first-qualifying across the seam) + BLIND-CONCURRENCY-001 +
// BLIND-RESOURCE-001. White-box (package contentsafety); reuses the scanner_test
// lexicon helpers.
package contentsafety

import (
	"strings"
	"sync"
	"testing"

	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

func gateGuardLexicon(t *testing.T) safetylexicon.Lexicon {
	t.Helper()
	return lexiconWith(t,
		enRow("lowword", safetylexicon.CategoryOther, safetylexicon.SeverityLow, 5001),
		enRow("medword", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium, 5002),
		enRow("highword", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh, 5003),
	)
}

// 8.4-UNIT-019 — gated termination: default (min=Medium) terminates on a high
// term split across deltas; loose (min=High) does NOT terminate on a medium term.
func TestStreamGuardMin_GatedTermination(t *testing.T) {
	s := NewScanner(gateGuardLexicon(t))

	// default: a HIGH term split across the seam → terminate.
	def := NewStreamGuardMin(s, safetylexicon.SeverityMedium)
	if _, idx := observeAll(def, []string{"say high", "word now"}); idx != 1 {
		t.Fatalf("default+high split: terminated at delta %d, want the completing delta (1)", idx)
	}

	// loose: a MEDIUM term (below the high threshold) → never terminate.
	loose := NewStreamGuardMin(s, safetylexicon.SeverityHigh)
	if m, idx := observeAll(loose, []string{"say med", "word now"}); idx != -1 {
		t.Fatalf("loose+medium must NOT terminate; terminated at %d on %q", idx, m.Canonical)
	}
}

// 8.4-UNIT-020 (G2 stream) — first-qualifying across the seam: under loose, a LOW
// term earlier in the stream must NOT short-circuit and mask a later HIGH term
// split across deltas. The guard terminates on the high (not the low).
func TestStreamGuardMin_FirstQualifyingAcrossSeam(t *testing.T) {
	s := NewScanner(gateGuardLexicon(t))
	g := NewStreamGuardMin(s, safetylexicon.SeverityHigh) // loose

	// Low term wholly in delta 0 (sub-threshold → no terminate); high term split
	// across deltas 1+2 → terminate on the completing delta with the HIGH match.
	deltas := []string{"pre lowword mid ", "high", "word post"}
	m, idx := observeAll(g, deltas)
	if idx != 2 {
		t.Fatalf("expected termination on the high-completing delta (2), got %d", idx)
	}
	if m.Canonical != "highword" {
		t.Fatalf("G2 stream: terminated on %q, want highword (the low must not mask the later high)", m.Canonical)
	}
}

// 8.4-UNIT — strict (min=Low) == NewStreamGuard block-all: a low term terminates
// under strict, matching the pre-8.4 default constructor.
func TestStreamGuardMin_StrictEqualsBlockAll(t *testing.T) {
	s := NewScanner(gateGuardLexicon(t))
	strict := NewStreamGuardMin(s, safetylexicon.SeverityLow)
	if _, hit := strict.Observe("contains lowword here"); !hit {
		t.Fatal("strict (min=Low) must terminate on a low term (block-all)")
	}
	// The default constructor is exactly the block-all guard.
	def := NewStreamGuard(s)
	if _, hit := def.Observe("contains lowword here"); !hit {
		t.Fatal("NewStreamGuard must be block-all (== NewStreamGuardMin(_, SeverityLow))")
	}
}

// 8.4-BLIND-CONCURRENCY-001 — N parallel per-request guards with DIFFERENT levels
// share one pure Scanner; run under -race. Each guard reaches its own verdict.
func TestStreamGuardMin_ConcurrentDifferentLevels(t *testing.T) {
	s := NewScanner(gateGuardLexicon(t))
	mins := []safetylexicon.Severity{safetylexicon.SeverityHigh, safetylexicon.SeverityMedium, safetylexicon.SeverityLow}

	var wg sync.WaitGroup
	for g := 0; g < 48; g++ {
		min := mins[g%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			guard := NewStreamGuardMin(s, min)
			// A medium term: blocks under medium/low (rank≥), passes under high.
			_, hit := guard.Observe("text with medword inside")
			wantHit := severityRank(safetylexicon.SeverityMedium) >= severityRank(min)
			if hit != wantHit {
				t.Errorf("min=%s: hit=%v want %v", min, hit, wantHit)
			}
		}()
	}
	wg.Wait()
}

// 8.4-BLIND-RESOURCE-001 — the severity gate does NOT change buffer growth: with a
// min threshold and a long benign stream, the buffer stays bounded by the window.
func TestStreamGuardMin_BufferBounded(t *testing.T) {
	s := NewScanner(gateGuardLexicon(t))
	g := NewStreamGuardMin(s, safetylexicon.SeverityHigh)
	for i := 0; i < 100; i++ {
		if _, hit := g.Observe(strings.Repeat("a", 100)); hit {
			t.Fatal("benign stream unexpectedly terminated")
		}
		if len(g.buf) > g.Window() {
			t.Fatalf("buffer grew to %d runes, exceeds window %d (gating must not change growth)", len(g.buf), g.Window())
		}
	}
}
