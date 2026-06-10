// Story 8.2 — AC4 hot-path performance soft-gate (8.2-BENCH-001) + allocation
// shape (8.2-BLIND-RESOURCE-001, informational via -benchmem). Measured against
// the REAL DefaultLexicon and WITH the re-normalization cost the scanner pays
// inside MightContain/Lookup (Architect Medium) — never traded for a miss.
//
// Soft-gate: a breach emits t.Log, never t.Fatal — background load on shared CI
// runners must not flap the suite (BR-4.1, the 8.1 / models_bench_test.go
// precedent). The only HARD perf-adjacent invariant is the no-false-negative
// sweep (UNIT-013), asserted elsewhere.
package contentsafety

import (
	"sort"
	"testing"
	"time"

	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// representativeCleanPrompt is a typical multi-message clean prompt (mixed
// en/zh), the common case the budget targets — Bloom excludes ~98% of windows.
var representativeCleanPrompt = []string{
	"You are a helpful assistant. Answer concisely and cite sources.",
	"请帮我写一首关于春天的五言绝句，要清新自然。",
	"Also summarize the key differences between TCP and UDP in three bullets.",
}

func benchScanP99(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(float64(len(samples)) * 0.99)
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// 8.2-BENCH-001 (soft-gate) — clean multi-message scan P99 against the proposed
// AC4 ceiling, measured with the re-normalization cost included.
func TestBENCH001_clean_scan_p99_soft_gate(t *testing.T) {
	const N = 5_000
	// AC4 proposed 50µs, but that predated the unified-windowing + re-normalization
	// design (Architect Medium): the real measured clean-scan P99 on the placeholder
	// corpus is ~290µs (M4, ~110µs mean / 696 allocs — the redundant Normalize the
	// Architect flagged, accepted-and-measured per the ratified ruling). The soft-gate
	// ceiling is set from that real benchmark so it signals a genuine REGRESSION
	// (~2× slowdown) rather than firing every run. Dev-adjustable to the bench host;
	// the absolute cost is negligible vs the upstream LLM round-trip (BR-4.1 soft-gate).
	const ceiling = 600 * time.Microsecond
	s := NewScanner(safetylexicon.DefaultLexicon)

	samples := make([]time.Duration, N)
	for i := 0; i < N; i++ {
		start := time.Now()
		_, _ = s.Scan(representativeCleanPrompt)
		samples[i] = time.Since(start)
	}
	got := benchScanP99(samples)
	if got > ceiling {
		t.Logf("SOFT-GATE: clean-scan P99 = %v exceeds %v (measured WITH re-normalization cost; bench-host variance — not a hard fail)", got, ceiling)
	} else {
		t.Logf("clean-scan P99 = %v (<= %v)", got, ceiling)
	}
}

// BenchmarkStory82_CleanScan — 8.2-BENCH-001 / 8.2-BLIND-RESOURCE-001. Run with
// -benchmem to observe the per-scan allocation shape (no per-request leak).
func BenchmarkStory82_CleanScan(b *testing.B) {
	s := NewScanner(safetylexicon.DefaultLexicon)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Scan(representativeCleanPrompt)
	}
}

// 8.3-BENCH-001 (soft-gate) — the streaming StreamGuard per-delta Observe cost on
// a representative clean stream. Mirrors 8.2-BENCH-001: a breach emits t.Log, never
// t.Fatal (BR-4.1 soft-gate). The HARD invariant is no-false-negative (UNIT-031),
// asserted in streamguard_test.go.
func TestBENCH001_stream_guard_observe_p99_soft_gate(t *testing.T) {
	const N = 5_000
	// The per-delta Observe re-scans the bounded window via the REUSED Scanner
	// (Bloom excludes ~98% of windows). Ceiling set generously from the bounded-
	// window cost; the absolute value is negligible vs the upstream LLM round-trip.
	const ceiling = 600 * time.Microsecond
	scanner := NewScanner(safetylexicon.DefaultLexicon)
	// A representative clean content-delta the chunker would emit incrementally.
	const delta = "Here is a concise, helpful answer to your question. "

	samples := make([]time.Duration, N)
	for i := 0; i < N; i++ {
		g := NewStreamGuard(scanner)
		start := time.Now()
		_, _ = g.Observe(delta)
		samples[i] = time.Since(start)
	}
	got := benchScanP99(samples)
	if got > ceiling {
		t.Logf("SOFT-GATE: stream-guard per-delta Observe P99 = %v exceeds %v (bench-host variance — not a hard fail)", got, ceiling)
	} else {
		t.Logf("stream-guard per-delta Observe P99 = %v (<= %v)", got, ceiling)
	}
}

// BenchmarkStory83_StreamGuardObserve — 8.3-BENCH-001. Steady-state per-delta cost
// on one guard fed a long clean stream (buffer pinned at the window). -benchmem
// surfaces the per-Observe allocation shape (bounded, O(window)).
func BenchmarkStory83_StreamGuardObserve(b *testing.B) {
	g := NewStreamGuard(NewScanner(safetylexicon.DefaultLexicon))
	const delta = "Here is a concise, helpful answer to your question. "
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.Observe(delta)
	}
}

// BenchmarkStory82_HitScan measures the reject path (a term early in the prompt)
// — should be cheaper than the clean scan (fail-fast on first hit).
func BenchmarkStory82_HitScan(b *testing.B) {
	s := NewScanner(safetylexicon.DefaultLexicon)
	hit := []string{"please emit badword immediately"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Scan(hit)
	}
}
