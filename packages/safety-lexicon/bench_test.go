package safetylexicon

import (
	"sort"
	"testing"
	"time"
)

// Story 8.1 AC4 / BR-4.4 / OQ-8.1-7 — performance regression markers.
//
// Lookup P99 < 2µs and MightContain P99 < 500ns are SOFT gates: a breach emits
// t.Log rather than t.Fatal so background load on shared CI runners cannot flap
// the suite (mirrors apps/api-gateway/internal/handlers/models_bench_test.go,
// Story 4.7). The numbers are Dev-adjustable to the bench host with a note; the
// only HARD perf-adjacent invariant is the Bloom FP ≤ 1% bound (UNIT-022).
//
// Callers are expected to Normalize once and reuse on the hot path; these
// benchmarks pass already-canonical terms so they measure the lookup/Bloom cost,
// not repeated normalization.

func benchTerms() []string {
	out := make([]string, len(DefaultLexicon.terms))
	for i, m := range DefaultLexicon.terms {
		out[i] = m.Canonical
	}
	return out
}

func BenchmarkLookup(b *testing.B) {
	terms := benchTerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = DefaultLexicon.Lookup(terms[i%len(terms)])
	}
}

func BenchmarkMightContain(b *testing.B) {
	terms := benchTerms()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = DefaultLexicon.MightContain(terms[i%len(terms)])
	}
}

func p99(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(float64(len(samples)) * 0.99)
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// 8.1-BENCH-001 (soft-gate)
func TestBENCH001_lookup_p99_under_2us(t *testing.T) {
	const N = 20_000
	const ceiling = 2 * time.Microsecond
	terms := benchTerms()
	samples := make([]time.Duration, N)
	for i := 0; i < N; i++ {
		term := terms[i%len(terms)]
		start := time.Now()
		_, _ = DefaultLexicon.Lookup(term)
		samples[i] = time.Since(start)
	}
	got := p99(samples)
	if got > ceiling {
		t.Logf("SOFT-GATE: Lookup P99 = %v exceeds %v (bench-host variance; not a hard fail)", got, ceiling)
	} else {
		t.Logf("Lookup P99 = %v (<= %v)", got, ceiling)
	}
}

// 8.1-BENCH-002 (soft-gate)
func TestBENCH002_mightcontain_p99_under_500ns(t *testing.T) {
	const N = 20_000
	const ceiling = 500 * time.Nanosecond
	terms := benchTerms()
	samples := make([]time.Duration, N)
	for i := 0; i < N; i++ {
		term := terms[i%len(terms)]
		start := time.Now()
		_ = DefaultLexicon.MightContain(term)
		samples[i] = time.Since(start)
	}
	got := p99(samples)
	if got > ceiling {
		t.Logf("SOFT-GATE: MightContain P99 = %v exceeds %v (bench-host variance; not a hard fail)", got, ceiling)
	} else {
		t.Logf("MightContain P99 = %v (<= %v)", got, ceiling)
	}
}
