package pricing

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// dec is a test helper: parse a decimal or fail.
func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("bad decimal %q: %v", s, err)
	}
	return d
}

// snap builds a one-model snapshot for golden-vector tests.
func snap(t *testing.T, model, in, out, markup string) *Snapshot {
	t.Helper()
	return NewSnapshot(map[string]Row{
		model: {PriceIn: dec(t, in), PriceOut: dec(t, out), Markup: dec(t, markup)},
	})
}

const perToken = billingv1.BillingMode_BILLING_MODE_PER_TOKEN

// computeFixed runs ComputeCost and returns the NUMERIC(12,4) string form.
func computeFixed(t *testing.T, s *Snapshot, model string, p, c uint32, mode billingv1.BillingMode) (string, Result, error) {
	t.Helper()
	r, err := s.ComputeCost(model, p, c, 0, mode)
	if err != nil {
		return "", r, err
	}
	return r.Cost.StringFixed(CostScale), r, nil
}

func TestComputeCost_GoldenVectors(t *testing.T) {
	tests := []struct {
		id      string
		model   string
		in, out string
		markup  string
		p, c    uint32
		want    string
	}{
		// 7.1-UNIT-004 — canonical AC1 worked example.
		{"UNIT-004 qwen-max", "qwen-max", "0.0080", "0.0240", "10.00", 1500, 800, "0.0343"},
		// 7.1-UNIT-005 — Example 1 + 15% boundary.
		{"UNIT-005 deepseek-v3", "deepseek-v3", "0.0014", "0.0028", "15.00", 10000, 5000, "0.0322"},
		// 7.1-UNIT-006 — markup lower bound 5.00 → ×1.05.
		{"UNIT-006 markup5", "m", "0.0100", "0.0100", "5.00", 1000, 1000, "0.0210"},
		// 7.1-UNIT-007 — markup upper bound 15.00 → ×1.15.
		{"UNIT-007 markup15", "m", "0.0100", "0.0100", "15.00", 1000, 1000, "0.0230"},
		// 7.1-UNIT-008 — default markup 10.00 → ×1.10.
		{"UNIT-008 markup10", "m", "0.0100", "0.0100", "10.00", 1000, 1000, "0.0220"},
		// 7.1-UNIT-009 — completion=0 (embeddings-style) → bills input only.
		{"UNIT-009 zero-completion", "m", "0.0080", "0.0240", "10.00", 1500, 0, "0.0132"},
		// 7.1-UNIT-010 — prompt=0 AND completion=0 → 0.0000.
		{"UNIT-010 empty", "m", "0.0080", "0.0240", "10.00", 0, 0, "0.0000"},
		// 7.1-UNIT-011 — sub-0.5-mill → 0.0000 (Example 2, round-to-zero / Low-issue).
		{"UNIT-011 round-to-zero", "m", "0.0080", "0.0000", "10.00", 1, 0, "0.0000"},
		// 7.1-UNIT-013 — large-token vector, no precision loss.
		{"UNIT-013 large", "m", "0.0040", "0.0120", "10.00", 1000000, 1000000, "17.6000"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			s := snap(t, tc.model, tc.in, tc.out, tc.markup)
			got, _, err := computeFixed(t, s, tc.model, tc.p, tc.c, perToken)
			if err != nil {
				t.Fatalf("ComputeCost: %v", err)
			}
			if got != tc.want {
				t.Fatalf("cost = %s, want %s", got, tc.want)
			}
		})
	}
}

// 7.1-UNIT-012 — HALF-UP at the .xxxx5 boundary rounds UP (never half-even, never
// down). base×mult = 0.00275 exactly → 0.0028.
func TestComputeCost_HalfUpBoundary(t *testing.T) {
	s := snap(t, "m", "0.0010", "0.0000", "10.00")
	got, _, err := computeFixed(t, s, "m", 2500, 0, perToken)
	if err != nil {
		t.Fatalf("ComputeCost: %v", err)
	}
	if got != "0.0028" {
		t.Fatalf("HALF-UP boundary cost = %s, want 0.0028", got)
	}
}

// 7.1-UNIT-014 [M-1] — Float-slip guard. The exact pre-round value 0.00275 is
// stored in float64 as 0.0027499999999999998; a naive float64 round-half-up
// would yield 0.0027 (a 1-mill under-charge). The Decimal path must get 0.0028
// byte-exact. This test fails loudly if anyone reintroduces float64 arithmetic
// on the money path (the M-1 routing-svc REUSE trap).
func TestComputeCost_FloatSlipGuard_M1(t *testing.T) {
	s := snap(t, "m", "0.0010", "0.0000", "10.00")
	got, _, err := computeFixed(t, s, "m", 2500, 0, perToken)
	if err != nil {
		t.Fatalf("ComputeCost: %v", err)
	}
	if got != "0.0028" {
		t.Fatalf("Decimal cost = %s, want 0.0028 (a truncating/half-even impl yields 0.0027)", got)
	}
	// Guard the rounding semantics that protect the money path: the exact
	// pre-round value 0.00275 must round HALF-UP to 0.0028. A truncating impl
	// (StringFixed/floor) would give 0.0027 — a 1-mill under-charge. This makes
	// the M-1 intent explicit and deterministic (no float64 representation luck).
	exact := dec(t, "0.00275")
	if up := exact.Round(CostScale).StringFixed(CostScale); up != "0.0028" {
		t.Fatalf("HALF-UP of 0.00275 = %s, want 0.0028", up)
	}
	if trunc := exact.Truncate(CostScale).StringFixed(CostScale); trunc != "0.0027" {
		t.Fatalf("truncation sanity: 0.00275 truncated = %s, want 0.0027", trunc)
	}
}

// 7.1-UNIT-015 — unknown model → typed ErrNoPricing (caller dead-letters; never
// zero-charge).
func TestComputeCost_ErrNoPricing(t *testing.T) {
	s := snap(t, "known", "0.0010", "0.0010", "10.00")
	_, _, err := computeFixed(t, s, "unknown", 100, 100, perToken)
	if err == nil {
		t.Fatal("expected ErrNoPricing for unknown model")
	}
	if err != ErrNoPricing {
		t.Fatalf("err = %v, want ErrNoPricing", err)
	}
	// Empty/nil snapshot also reports ErrNoPricing, never panics.
	var nilSnap *Snapshot
	if _, err := nilSnap.ComputeCost("m", 1, 1, 0, perToken); err != ErrNoPricing {
		t.Fatalf("nil snapshot err = %v, want ErrNoPricing", err)
	}
}

// 7.1-UNIT-016 — markup ∉ [5,15] → MarkupOutOfRange flag set, but billed AS-IS
// (never drop the charge). Tests both bounds (4.99 / 15.01).
func TestComputeCost_MarkupOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		markup string
		want   string // 1000/1000*0.01 + 0 = 0.01 base; ×(1+markup/100)
	}{
		{"4.99", "0.0105"},  // 0.01 × 1.0499 = 0.010499 → 0.0105
		{"15.01", "0.0115"}, // 0.01 × 1.1501 = 0.011501 → 0.0115
	} {
		s := snap(t, "m", "0.0100", "0.0000", tc.markup)
		got, res, err := computeFixed(t, s, "m", 1000, 0, perToken)
		if err != nil {
			t.Fatalf("ComputeCost: %v", err)
		}
		if !res.MarkupOutOfRange {
			t.Fatalf("markup %s: expected MarkupOutOfRange=true", tc.markup)
		}
		if got != tc.want {
			t.Fatalf("markup %s: cost = %s, want %s (billed as-is)", tc.markup, got, tc.want)
		}
	}
	// In-range markup never flags.
	s := snap(t, "m", "0.0100", "0.0000", "10.00")
	_, res, _ := computeFixed(t, s, "m", 1000, 0, perToken)
	if res.MarkupOutOfRange {
		t.Fatal("in-range markup 10.00 must not flag out-of-range")
	}
}

// 7.1-UNIT-017 — per_call_price_usd column present but mode is PER_TOKEN →
// per-token is the billed mode (Q-PERCALL OFF in 7.1; forward-compat only).
func TestComputeCost_PerCallColumnUnwired(t *testing.T) {
	pc := dec(t, "0.5000")
	s := NewSnapshot(map[string]Row{
		"m": {PriceIn: dec(t, "0.0080"), PriceOut: dec(t, "0.0240"), Markup: dec(t, "10.00"), PerCallPrice: &pc},
	})
	// mode PER_TOKEN ignores per_call → the AC1 golden 0.0343, NOT 0.5000.
	got, _, err := computeFixed(t, s, "m", 1500, 800, perToken)
	if err != nil {
		t.Fatalf("ComputeCost: %v", err)
	}
	if got != "0.0343" {
		t.Fatalf("per-token cost = %s, want 0.0343 (per_call must be ignored)", got)
	}
	// Forward-compat: PER_CALL mode REPLACES per-token with the flat fee.
	gotPC, _, err := computeFixed(t, s, "m", 1500, 800, billingv1.BillingMode_BILLING_MODE_PER_CALL)
	if err != nil {
		t.Fatalf("ComputeCost per-call: %v", err)
	}
	if gotPC != "0.5000" {
		t.Fatalf("per-call cost = %s, want 0.5000 (flat fee replaces per-token)", gotPC)
	}
}

// 7.1-UNIT-018 — purity (BR-C-5): identical inputs → identical Decimal, no I/O,
// no clock. The golden-suite seam invariant.
func TestComputeCost_Purity(t *testing.T) {
	s := snap(t, "qwen-max", "0.0080", "0.0240", "10.00")
	var first string
	for i := 0; i < 50; i++ {
		got, _, err := computeFixed(t, s, "qwen-max", 1500, 800, perToken)
		if err != nil {
			t.Fatalf("ComputeCost: %v", err)
		}
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("non-deterministic: iteration %d = %s, first = %s", i, got, first)
		}
	}
	if first != "0.0343" {
		t.Fatalf("purity baseline = %s, want 0.0343", first)
	}
}

// 7.1-BLIND-BOUNDARY-002 — max uint32 tokens → Decimal mult, no overflow.
func TestComputeCost_MaxUint32Tokens(t *testing.T) {
	s := snap(t, "m", "0.0010", "0.0010", "10.00")
	// base = (2^32-1)/1000 × 0.001 × 2 ≈ 8589.9345; ×1.1 ≈ 9448.928...
	got, _, err := computeFixed(t, s, "m", math.MaxUint32, math.MaxUint32, perToken)
	if err != nil {
		t.Fatalf("ComputeCost: %v", err)
	}
	want := decimal.NewFromInt(int64(math.MaxUint32)).Div(thousand).Mul(dec(t, "0.0010")).
		Mul(decimal.NewFromInt(2)).Mul(dec(t, "1.10")).Round(CostScale).StringFixed(CostScale)
	if got != want {
		t.Fatalf("max-uint32 cost = %s, want %s (no overflow/precision loss)", got, want)
	}
}
