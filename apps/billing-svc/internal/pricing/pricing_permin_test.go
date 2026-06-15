// Story 9.6 (T5.3) — PER_MINUTE (ASR) cost-engine tests. The ceil-to-second
// formula (Q-ASR-BILLING RATIFIED):
//
//	cost = ceil(duration_seconds) × price_per_minute_audio_usd/60 × (1+markup)
//
// HALF-UP NUMERIC(12,4), shopspring/decimal only (BR-C-5). Fail-CLOSED when a
// PER_MINUTE event has no per-minute price (never a silent zero — BR-D-3).
package pricing

import (
	"testing"

	"github.com/shopspring/decimal"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

const perMinute = billingv1.BillingMode_BILLING_MODE_PER_MINUTE

func asrSnap(t *testing.T, pricePerMin, markup string) *Snapshot {
	t.Helper()
	pm := dec(t, pricePerMin)
	return NewSnapshot(map[string]Row{
		"doubao-asr": {
			PriceIn:             decimal.Zero, // token columns 0 for ASR rows (Q-BILLING Amend 1)
			PriceOut:            decimal.Zero,
			Markup:              dec(t, markup),
			PricePerMinuteAudio: &pm,
		},
	})
}

// 9.6-UNIT-016 — ceil-to-second boundary table at price 0.006/min, markup 10%.
func TestComputeCost_PerMinute_BoundaryTable(t *testing.T) {
	s := asrSnap(t, "0.006000", "10.00")
	// cost = ceil(sec) × 0.006/60 × 1.10 = ceil(sec) × 0.0001 × 1.10 = ceil(sec) × 0.00011
	cases := []struct {
		name    string
		seconds float64
		want    string // NUMERIC(12,4) HALF-UP
	}{
		{"0.0s → 0 (no charge for zero — caller fences earlier)", 0.0, "0.0000"},
		{"0.4s → ceil 1s", 0.4, "0.0001"},       // 1×0.00011 = 0.00011 → 0.0001
		{"1.0s → 1s", 1.0, "0.0001"},            // 0.00011 → 0.0001
		{"3.2s → ceil 4s", 3.2, "0.0004"},       // 4×0.00011 = 0.00044 → 0.0004
		{"60.0s → 60s = 1 min", 60.0, "0.0066"}, // 60×0.00011 = 0.0066
		{"61.0s → 61s", 61.0, "0.0067"},         // 61×0.00011 = 0.00671 → 0.0067
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.ComputeCost(CostInput{ModelID: "doubao-asr", AudioDurationSeconds: c.seconds, Mode: perMinute})
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if got := res.Cost.StringFixed(CostScale); got != c.want {
				t.Fatalf("seconds=%v cost=%s, want %s", c.seconds, got, c.want)
			}
		})
	}
}

// 9.6-UNIT-019 / UNIT-021 — fail-closed: a PER_MINUTE event for a model with NO
// per-minute price → ErrNoPricing (never a silent zero-cost ledger row).
func TestComputeCost_PerMinute_NoPrice_FailClosed(t *testing.T) {
	// A token model (no PricePerMinuteAudio) billed PER_MINUTE → ErrNoPricing.
	tokenSnap := NewSnapshot(map[string]Row{
		"deepseek-v3": {PriceIn: dec(t, "0.0014"), PriceOut: dec(t, "0.0028"), Markup: dec(t, "10.00")},
	})
	if _, err := tokenSnap.ComputeCost(CostInput{ModelID: "deepseek-v3", AudioDurationSeconds: 5.0, Mode: perMinute}); err != ErrNoPricing {
		t.Fatalf("err=%v, want ErrNoPricing (fail-closed, no silent zero)", err)
	}
	// A missing model → ErrNoPricing regardless of mode.
	if _, err := tokenSnap.ComputeCost(CostInput{ModelID: "doubao-asr", AudioDurationSeconds: 5.0, Mode: perMinute}); err != ErrNoPricing {
		t.Fatalf("missing model err=%v, want ErrNoPricing", err)
	}
}

// 9.6-UNIT-018 — the signature extension: PER_TOKEN / PER_CALL branches IGNORE
// the duration argument (it only affects PER_MINUTE).
func TestComputeCost_DurationIgnoredByTokenModes(t *testing.T) {
	s := snap(t, "qwen-max", "0.0080", "0.0240", "10.00")
	// PER_TOKEN with a nonzero duration must equal PER_TOKEN with zero duration.
	a, err := s.ComputeCost(CostInput{ModelID: "qwen-max", PromptTokens: 1500, CompletionTokens: 800, AudioDurationSeconds: 0, Mode: perToken})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ComputeCost(CostInput{ModelID: "qwen-max", PromptTokens: 1500, CompletionTokens: 800, AudioDurationSeconds: 9999, Mode: perToken})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Cost.Equal(b.Cost) {
		t.Fatalf("PER_TOKEN cost changed with duration: %s vs %s", a.Cost, b.Cost)
	}
}

// 9.6-UNIT-016 — the canonical story worked example (3.2s @ 0.006/min, 10%).
func TestComputeCost_PerMinute_StoryExample(t *testing.T) {
	s := asrSnap(t, "0.006000", "10.00")
	res, err := s.ComputeCost(CostInput{ModelID: "doubao-asr", AudioDurationSeconds: 3.2, Mode: perMinute})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Cost.StringFixed(CostScale); got != "0.0004" {
		t.Fatalf("story example cost=%s, want 0.0004", got)
	}
}
