// Package scoring is the Story-6.2 Scorer seam for the quality + latency
// routing strategies (AC3, Q-A Option A). A Scorer maps candidate model ids to
// a numeric score and reports whether it actually has data.
//
// The quality backing store (benchmark_results) and the latency backing store
// (request_logs_hourly_agg) are ClickHouse tables that do NOT exist until Epic
// 9 — Epic 6's prerequisites are [3,4], which exclude Epic 9. So the default
// quality/latency Scorers report hasData=false (NoData), and the strategies
// degrade DETERMINISTICALLY to the cost ordering (BR3-1/3-2). When Epic 9 lands
// the ClickHouse data, a real Scorer drops in behind this exact interface with
// NO engine or contract change (BR3-1) — the only edit is the constructor
// wiring in cmd/server/main.go.
package scoring

import (
	"context"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
)

// Scorer assigns a score to each candidate it has data for. The bool reports
// data-availability: false → the strategy degrades to the cost ordering;
// true → the strategy ranks by the returned scores (quality desc / latency
// asc). Implementations MUST be pure + deterministic for a given input.
type Scorer interface {
	Score(ctx context.Context, candidates []engine.ModelEntry) (scores map[string]float64, hasData bool)
}

// NoData is the default quality/latency Scorer until Epic 9: it always reports
// no data, so the strategy degrades to the deterministic cost-then-alphabetical
// fallback (score_source=fallback). It is the BR3-1 "Epic-6 path".
type NoData struct{}

// Score always returns (nil, false).
func (NoData) Score(context.Context, []engine.ModelEntry) (map[string]float64, bool) {
	return nil, false
}

// Map is a data-bearing Scorer backed by a static model_id → score map. It is
// the drop-in shape a real Epic-9 ClickHouse Scorer mirrors (BR3-1 seam proof)
// and the seeded Scorer used to exercise the real-scoring branch in tests.
// hasData is true whenever the map is non-empty.
type Map map[string]float64

// Score returns the static scores and hasData=len(m) > 0.
func (m Map) Score(context.Context, []engine.ModelEntry) (map[string]float64, bool) {
	if len(m) == 0 {
		return nil, false
	}
	return m, true
}
