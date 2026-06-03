package strategy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
)

// Story 6.3 AC1 — RankedStrategy impls. The ranked lift returns the FULL
// concrete candidate order (rank-1 first); the engine derives selected=chain[0]
// + failover_chain=chain[1:]. These tests assert the WHOLE-chain contract:
// ordering, he-router-* whole-chain exclusion (BR1-3), chain[0] parity with the
// 6.2 winner (BR1-4), determinism, and de-dup (BR1-5).

func ids(chain []engine.ModelEntry) []string {
	out := make([]string, len(chain))
	for i, m := range chain {
		out[i] = m.ID
	}
	return out
}

func eqIDs(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 6.3-UNIT-001 (P0) — cost ranks ALL concrete candidates price-ascending; the
// chain is the full ordered list (rank-1 first).
func Test_UNIT_001_cost_ranks_all_price_ascending(t *testing.T) {
	t.Parallel()
	c := strategy.NewCost(priced(map[string]float64{"a": 0.03, "b": 0.01, "c": 0.02}))
	chain, src, err := c.SelectRanked(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	if got := ids(chain); !eqIDs(got, "b", "c", "a") {
		t.Errorf("chain = %v, want [b c a] (price asc)", got)
	}
	if src != engine.ScoreSourceModelPricing {
		t.Errorf("score_source = %q, want model_pricing", src)
	}
}

// 6.3-UNIT-002 (P0) — price tie → first-alphabetical; deterministic whole-chain
// ordering (Q-J).
func Test_UNIT_002_cost_price_tie_alphabetical(t *testing.T) {
	t.Parallel()
	// b and c tie at 0.02; a is cheapest; d is dearest.
	c := strategy.NewCost(priced(map[string]float64{"a": 0.01, "c": 0.02, "b": 0.02, "d": 0.05}))
	chain, _, err := c.SelectRanked(context.Background(), cands("d", "c", "b", "a"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	if got := ids(chain); !eqIDs(got, "a", "b", "c", "d") {
		t.Errorf("chain = %v, want [a b c d] (price asc, tie alphabetical)", got)
	}
}

// 6.3-UNIT-003 (P0) — quality real-score → score-ranked chain (quality_score desc).
func Test_UNIT_003_quality_real_ranks_desc(t *testing.T) {
	t.Parallel()
	q := strategy.NewQuality(scoring.Map{"a": 0.9, "b": 0.5, "c": 0.7}, nil)
	chain, src, err := q.SelectRanked(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	if got := ids(chain); !eqIDs(got, "a", "c", "b") {
		t.Errorf("chain = %v, want [a c b] (quality desc)", got)
	}
	if src != engine.ScoreSourceClickHouse {
		t.Errorf("score_source = %q, want clickhouse", src)
	}
}

// 6.3-UNIT-004 (P0) — latency real-score → score-ranked chain (p95 asc).
func Test_UNIT_004_latency_real_ranks_asc(t *testing.T) {
	t.Parallel()
	l := strategy.NewLatency(scoring.Map{"a": 300, "b": 100, "c": 200}, nil)
	chain, src, err := l.SelectRanked(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	if got := ids(chain); !eqIDs(got, "b", "c", "a") {
		t.Errorf("chain = %v, want [b c a] (latency asc)", got)
	}
	if src != engine.ScoreSourceClickHouse {
		t.Errorf("score_source = %q, want clickhouse", src)
	}
}

// 6.3-UNIT-005 (P0) — quality/latency DEGRADED (no scorer data) → deterministic
// cost-then-alphabetical chain; score_source=fallback (6.2 BR3-2).
func Test_UNIT_005_degraded_ranks_cost_then_alphabetical(t *testing.T) {
	t.Parallel()
	prices := priced(map[string]float64{"a": 3, "b": 1, "c": 2})
	q := strategy.NewQuality(scoring.NoData{}, prices)
	chain, src, err := q.SelectRanked(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("quality SelectRanked: %v", err)
	}
	if got := ids(chain); !eqIDs(got, "b", "c", "a") {
		t.Errorf("quality degraded chain = %v, want [b c a] (cost asc)", got)
	}
	if src != engine.ScoreSourceFallback {
		t.Errorf("quality degraded score_source = %q, want fallback", src)
	}

	l := strategy.NewLatency(scoring.NoData{}, prices)
	lchain, lsrc, err := l.SelectRanked(context.Background(), cands("a", "b", "c"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("latency SelectRanked: %v", err)
	}
	if got := ids(lchain); !eqIDs(got, "b", "c", "a") {
		t.Errorf("latency degraded chain = %v, want [b c a] (cost asc)", got)
	}
	if lsrc != engine.ScoreSourceFallback {
		t.Errorf("latency degraded score_source = %q, want fallback", lsrc)
	}
}

// 6.3-UNIT-008 (P0) — ⚠️ NO he-router-* id appears ANYWHERE in the chain; the
// whole ranking is concrete-only (BR1-3 correctness gate).
func Test_UNIT_008_no_meta_model_anywhere_in_chain(t *testing.T) {
	t.Parallel()
	// catalogue mixes 3 virtual he-router-* entries among concrete models.
	in := cands("he-router-cost", "a", "he-router-quality", "b", "he-router-latency", "c")
	prices := priced(map[string]float64{"a": 0.03, "b": 0.01, "c": 0.02})

	for _, tc := range []struct {
		name string
		run  func() ([]engine.ModelEntry, error)
	}{
		{"cost", func() ([]engine.ModelEntry, error) {
			ch, _, err := strategy.NewCost(prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
			return ch, err
		}},
		{"quality-degraded", func() ([]engine.ModelEntry, error) {
			ch, _, err := strategy.NewQuality(scoring.NoData{}, prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
			return ch, err
		}},
		{"quality-real", func() ([]engine.ModelEntry, error) {
			ch, _, err := strategy.NewQuality(scoring.Map{"a": 0.9, "b": 0.5, "c": 0.7}, prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
			return ch, err
		}},
		{"latency-degraded", func() ([]engine.ModelEntry, error) {
			ch, _, err := strategy.NewLatency(scoring.NoData{}, prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
			return ch, err
		}},
	} {
		chain, err := tc.run()
		if err != nil {
			t.Fatalf("%s: SelectRanked err = %v", tc.name, err)
		}
		if len(chain) != 3 {
			t.Errorf("%s: chain len = %d, want 3 (concrete only)", tc.name, len(chain))
		}
		for _, m := range chain {
			if strings.HasPrefix(m.ID, "he-router-") {
				t.Errorf("%s: he-router-* id %q leaked into chain %v (BR1-3 violation)", tc.name, m.ID, ids(chain))
			}
		}
	}
}

// 6.3-UNIT-009 + 6.3-UNIT-013 (P0/P1) — chain[0] == the 6.2 SelectSourced winner
// for the SAME input across cost/quality/latency (zero regression — BR1-4).
func Test_UNIT_009_chain_head_parity_with_winner(t *testing.T) {
	t.Parallel()
	in := cands("a", "b", "c")
	prices := priced(map[string]float64{"a": 0.03, "b": 0.01, "c": 0.02})

	check := func(name string, winner engine.ModelEntry, wsrc string, werr error, chain []engine.ModelEntry, csrc string, cerr error) {
		if werr != nil || cerr != nil {
			t.Fatalf("%s: winner err=%v ranked err=%v", name, werr, cerr)
		}
		if len(chain) == 0 || chain[0].ID != winner.ID {
			t.Errorf("%s: chain[0] = %v, want winner %q (BR1-4 parity)", name, ids(chain), winner.ID)
		}
		if csrc != wsrc {
			t.Errorf("%s: ranked source %q != winner source %q (Q-J)", name, csrc, wsrc)
		}
	}

	cw, cwsrc, cwe := strategy.NewCost(prices).SelectSourced(context.Background(), in, engine.SelectionHints{})
	cc, ccsrc, cce := strategy.NewCost(prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
	check("cost", cw, cwsrc, cwe, cc, ccsrc, cce)

	qScorer := scoring.Map{"a": 0.9, "b": 0.5, "c": 0.7}
	qw, qwsrc, qwe := strategy.NewQuality(qScorer, prices).SelectSourced(context.Background(), in, engine.SelectionHints{})
	qc, qcsrc, qce := strategy.NewQuality(qScorer, prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
	check("quality", qw, qwsrc, qwe, qc, qcsrc, qce)

	lw, lwsrc, lwe := strategy.NewLatency(scoring.NoData{}, prices).SelectSourced(context.Background(), in, engine.SelectionHints{})
	lc, lcsrc, lce := strategy.NewLatency(scoring.NoData{}, prices).SelectRanked(context.Background(), in, engine.SelectionHints{})
	check("latency-degraded", lw, lwsrc, lwe, lc, lcsrc, lce)
}

// 6.3-UNIT-010 (P0) — the ranked chain holds every concrete candidate exactly
// once (de-dup / no drop — BR1-5 chain shape).
func Test_UNIT_010_chain_is_dedup_total_cover(t *testing.T) {
	t.Parallel()
	in := cands("a", "b", "c", "d")
	chain, _, err := strategy.NewCost(priced(map[string]float64{"a": 1, "b": 2, "c": 3, "d": 4})).
		SelectRanked(context.Background(), in, engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	seen := map[string]int{}
	for _, m := range chain {
		seen[m.ID]++
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if seen[id] != 1 {
			t.Errorf("id %q appears %d times, want exactly 1 (de-dup + total cover)", id, seen[id])
		}
	}
}

// 6.3-BLIND-BOUNDARY-002 (P1) — single concrete candidate → chain of length 1
// (the winner; empty failover tail derived by the engine).
func Test_BLIND_BOUNDARY_002_single_candidate_chain_len_one(t *testing.T) {
	t.Parallel()
	chain, _, err := strategy.NewCost(priced(map[string]float64{"only": 0.01})).
		SelectRanked(context.Background(), cands("only"), engine.SelectionHints{})
	if err != nil {
		t.Fatalf("SelectRanked: %v", err)
	}
	if len(chain) != 1 || chain[0].ID != "only" {
		t.Errorf("chain = %v, want [only]", ids(chain))
	}
}
