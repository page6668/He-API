package strategy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
)

// 6.2-UNIT-019 — quality scorer WITH data → rank by quality_score DESC.
func TestQuality_WithData_RanksDesc(t *testing.T) {
	t.Parallel()
	q := strategy.NewQuality(scoring.Map{"a": 0.9, "b": 0.5, "c": 0.7}, nil)
	got, src, err := q.SelectSourced(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("selected = %q, want a (highest quality)", got.ID)
	}
	// 6.2-UNIT-024 — real scores → score_source=clickhouse.
	if src != engine.ScoreSourceClickHouse {
		t.Errorf("score_source = %q, want clickhouse", src)
	}
}

// 6.2-UNIT-020 — latency scorer WITH data → rank by p95_latency_ms ASC.
func TestLatency_WithData_RanksAsc(t *testing.T) {
	t.Parallel()
	l := strategy.NewLatency(scoring.Map{"a": 900, "b": 120, "c": 350}, nil)
	got, src, err := l.SelectSourced(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "b" {
		t.Errorf("selected = %q, want b (lowest p95)", got.ID)
	}
	if src != engine.ScoreSourceClickHouse {
		t.Errorf("score_source = %q, want clickhouse", src)
	}
}

// 6.2-UNIT-021 / AC3 example 1 — quality scorer NO data → degrade DETERMINISTICALLY
// to cost ordering; strategy_used stays QUALITY (engine-level); score_source=fallback.
func TestQuality_NoData_DegradesToCost(t *testing.T) {
	t.Parallel()
	// catalogue [a,b,c] priced a=$3,b=$1,c=$2 → cheapest = b.
	q := strategy.NewQuality(scoring.NoData{}, priced(map[string]float64{"a": 3, "b": 1, "c": 2}))
	got, src, err := q.SelectSourced(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "b" {
		t.Errorf("selected = %q, want b (degraded → cheapest)", got.ID)
	}
	if src != engine.ScoreSourceFallback {
		t.Errorf("score_source = %q, want fallback", src)
	}
}

// 6.2-UNIT-022 / AC3 example 2 — latency NO data + all prices equal → first-alphabetical.
func TestLatency_NoData_AllPricesEqual_Alphabetical(t *testing.T) {
	t.Parallel()
	l := strategy.NewLatency(nil, priced(map[string]float64{"a": 1, "b": 1, "c": 1}))
	got, src, err := l.SelectSourced(context.Background(), cands("c", "b", "a"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "a" {
		t.Errorf("selected = %q, want a (alphabetical degradation)", got.ID)
	}
	if src != engine.ScoreSourceFallback {
		t.Errorf("score_source = %q, want fallback", src)
	}
}

// 6.2-UNIT-025 / 6.2-BLIND-BOUNDARY-002 — the DEGRADED quality/latency path ALSO
// excludes he-router-*; an all-meta catalogue → ErrNoCandidates; a real-data
// path never returns a he-router-* id either.
func TestQualityLatency_ExcludesHeRouter(t *testing.T) {
	t.Parallel()
	// Degraded path: he-router-* must be excluded even though it could be "cheapest".
	q := strategy.NewQuality(scoring.NoData{}, priced(map[string]float64{"he-router-quality": 0.0, "qwen-max": 0.4}))
	got, _, err := q.SelectSourced(context.Background(), cands("he-router-quality", "qwen-max"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced: %v", err)
	}
	if got.ID != "qwen-max" {
		t.Errorf("degraded selected = %q, want qwen-max (he-router-* excluded)", got.ID)
	}

	// Real-data path: even if a he-router-* is scored best, it must be excluded.
	l := strategy.NewLatency(scoring.Map{"he-router-latency": 1, "deepseek-v3": 50}, nil)
	got2, _, err := l.SelectSourced(context.Background(), cands("he-router-latency", "deepseek-v3"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectSourced real: %v", err)
	}
	if got2.ID != "deepseek-v3" {
		t.Errorf("scored selected = %q, want deepseek-v3 (he-router-* excluded from scoring too)", got2.ID)
	}

	// All-meta catalogue → ErrNoCandidates on both branches.
	_, _, err = q.SelectSourced(context.Background(), cands("he-router-quality", "he-router-cost"), engine.SelectionHints{})
	if !errors.Is(err, engine.ErrNoCandidates) {
		t.Errorf("all-meta degraded err = %v, want ErrNoCandidates", err)
	}
}

// 6.2-UNIT-023 — strategy_used truthfulness is an engine-level concern; here we
// assert the strategy itself never reports model_pricing/default for quality
// (it reports clickhouse with data, fallback without) so the engine can echo
// the requested strategy while score_source distinguishes degradation.
func TestQuality_ScoreSourceNeverDefault(t *testing.T) {
	t.Parallel()
	withData := strategy.NewQuality(scoring.Map{"a": 1}, nil)
	if _, src, _ := withData.SelectSourced(context.Background(), cands("a", "b"), engine.SelectionHints{}); src != engine.ScoreSourceClickHouse {
		t.Errorf("with-data score_source = %q, want clickhouse", src)
	}
	noData := strategy.NewQuality(nil, nil)
	if _, src, _ := noData.SelectSourced(context.Background(), cands("a", "b"), engine.SelectionHints{}); src != engine.ScoreSourceFallback {
		t.Errorf("no-data score_source = %q, want fallback", src)
	}
}

// 6.2-UNIT-026 — a seeded test Scorer drops in WITHOUT any engine/contract
// change (BR3-1 seam proof): scoring.Map satisfies scoring.Scorer and flips the
// strategy from fallback to clickhouse with no other edit.
func TestScorerSeam_DropInProvesEpic9Readiness(t *testing.T) {
	t.Parallel()
	var _ scoring.Scorer = scoring.Map{}    // compile-time: Map is a Scorer
	var _ scoring.Scorer = scoring.NoData{} // compile-time: NoData is a Scorer

	base := cands("a", "b")
	degraded := strategy.NewLatency(scoring.NoData{}, nil)
	seeded := strategy.NewLatency(scoring.Map{"a": 10, "b": 5}, nil)

	if _, src, _ := degraded.SelectSourced(context.Background(), base, engine.SelectionHints{}); src != engine.ScoreSourceFallback {
		t.Errorf("degraded src = %q, want fallback", src)
	}
	got, src, _ := seeded.SelectSourced(context.Background(), base, engine.SelectionHints{})
	if src != engine.ScoreSourceClickHouse || got.ID != "b" {
		t.Errorf("seeded src=%q sel=%q, want clickhouse + b", src, got.ID)
	}
}
