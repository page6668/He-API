// Story 8.5 — AC4 persist-overhead soft-gate (8.5-BENCH-001). The ONLY thing 8.5
// adds to a request path is a non-blocking channel enqueue on the RARE block path
// (Record); the clean path is untouched. A breach emits t.Log, never t.Fatal —
// bench-host variance must not flap CI (the 8.2 BENCH-001 precedent, BR-4.1).
package safetylog

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v3"
)

func p99(samples []time.Duration) time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	idx := int(float64(len(samples)) * 0.99)
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

// 8.5-BENCH-001 (soft-gate) — Record (the block-path enqueue) P99 vs a ceiling.
// Measured WITHOUT a draining worker so the timing reflects the pure enqueue /
// drop cost the handler actually pays (the DB INSERT happens off-path on the
// worker goroutine and is never on the block path).
func TestBENCH001_persist_enqueue_p99_soft_gate(t *testing.T) {
	const N = 5_000
	// Generous ceiling: a buffered-channel send (or the drop branch once full) is
	// tens of ns; 50µs signals a genuine regression, not normal variance. The
	// absolute cost is negligible vs the upstream LLM round-trip.
	const ceiling = 50 * time.Microsecond

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	r := newRecorder(mock, discardLogger()) // no worker → measure pure enqueue/drop
	ev := inputEvent()

	samples := make([]time.Duration, N)
	for i := 0; i < N; i++ {
		start := time.Now()
		r.Record(context.Background(), ev)
		samples[i] = time.Since(start)
	}
	got := p99(samples)
	if got > ceiling {
		t.Logf("SOFT-GATE: persist enqueue P99 = %v exceeds %v (bench-host variance — not a hard fail)", got, ceiling)
		return
	}
	t.Logf("persist enqueue P99 = %v (under %v)", got, ceiling)
}
