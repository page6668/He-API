package scoring_test

import (
	"context"
	"testing"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/scoring"
)

func cands(ids ...string) []engine.ModelEntry {
	out := make([]engine.ModelEntry, len(ids))
	for i, id := range ids {
		out[i] = engine.ModelEntry{ID: id}
	}
	return out
}

// NoData always reports no data → drives the deterministic fallback (BR3-1).
func TestNoData_AlwaysNoData(t *testing.T) {
	t.Parallel()
	scores, ok := scoring.NoData{}.Score(context.Background(), cands("a", "b"))
	if ok {
		t.Errorf("NoData hasData = true, want false")
	}
	if scores != nil {
		t.Errorf("NoData scores = %v, want nil", scores)
	}
}

// 6.2-UNIT-026 — Map is a data-bearing Scorer; a non-empty map reports hasData;
// an empty map reports no-data (so it degrades, never a false-positive).
func TestMap_DataAvailability(t *testing.T) {
	t.Parallel()
	scores, ok := scoring.Map{"a": 0.9}.Score(context.Background(), cands("a"))
	if !ok {
		t.Errorf("non-empty Map hasData = false, want true")
	}
	if scores["a"] != 0.9 {
		t.Errorf("Map score[a] = %v, want 0.9", scores["a"])
	}

	if _, ok := (scoring.Map{}).Score(context.Background(), cands("a")); ok {
		t.Errorf("empty Map hasData = true, want false")
	}
	if _, ok := (scoring.Map(nil)).Score(context.Background(), nil); ok {
		t.Errorf("nil Map hasData = true, want false")
	}
}

// Compile-time: both implement scoring.Scorer (seam contract, BR3-1).
func TestScorerInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	var _ scoring.Scorer = scoring.NoData{}
	var _ scoring.Scorer = scoring.Map{}
}
