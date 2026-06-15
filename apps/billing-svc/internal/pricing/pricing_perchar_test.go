// Story 9.7 (T5.3) — PER_CHARACTER (TTS) cost-engine tests. The per-1k-chars
// formula (Q-TTS-BILLING RATIFIED):
//
//	cost = (character_count/1000) × price_per_1k_chars_audio_usd × (1+markup)
//
// HALF-UP NUMERIC(12,4), shopspring/decimal only (BR-C-5 / BR-3.4). Fail-CLOSED
// when a PER_CHARACTER event has no per-1k-chars price (never a silent zero —
// BR-D-3). char count = gateway-computed runes (the gateway is the sole
// authority — BR-4.5; the rune-count itself is unit-tested in the gateway
// handler suite).
package pricing

import (
	"testing"

	"github.com/shopspring/decimal"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

const perChar = billingv1.BillingMode_BILLING_MODE_PER_CHARACTER

func ttsSnap(t *testing.T, pricePer1kChars, markup string) *Snapshot {
	t.Helper()
	pc := dec(t, pricePer1kChars)
	return NewSnapshot(map[string]Row{
		"doubao-tts": {
			PriceIn:            decimal.Zero, // token columns 0 for TTS rows
			PriceOut:           decimal.Zero,
			Markup:             dec(t, markup),
			PricePerCharsAudio: &pc,
		},
	})
}

// 9.7-UNIT-020 — per-character boundary table at price 0.015/1k chars, markup 10%.
func TestComputeCost_PerCharacter_BoundaryTable(t *testing.T) {
	s := ttsSnap(t, "0.015000", "10.00")
	cases := []struct {
		name  string
		chars uint32
		want  string // NUMERIC(12,4) HALF-UP
	}{
		{"0 chars → 0", 0, "0.0000"},          // caller fences empty input earlier
		{"1 char → round-to-zero", 1, "0.0000"}, // 0.001×0.015×1.1 = 0.0000165 → 0.0000
		{"1000 chars → 1k unit", 1000, "0.0165"}, // 1×0.015×1.1 = 0.0165
		{"4096 chars → cap", 4096, "0.0676"},     // 4.096×0.015×1.1 = 0.067584 → 0.0676
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.ComputeCost(CostInput{ModelID: "doubao-tts", CharacterCount: c.chars, Mode: perChar})
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if got := res.Cost.StringFixed(CostScale); got != c.want {
				t.Fatalf("chars=%d cost=%s, want %s", c.chars, got, c.want)
			}
		})
	}
}

// 9.7-UNIT-022 — the canonical story worked example: 4 chars, 0.015/1k, markup
// 10% → (4/1000)×0.015×1.10 = 0.000066 → ledger 0.0001 (NUMERIC(12,4) HALF-UP).
func TestComputeCost_PerCharacter_StoryExample(t *testing.T) {
	s := ttsSnap(t, "0.015000", "10.00")
	res, err := s.ComputeCost(CostInput{ModelID: "doubao-tts", CharacterCount: 4, Mode: perChar})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Cost.StringFixed(CostScale); got != "0.0001" {
		t.Fatalf("story example cost=%s, want 0.0001", got)
	}
}

// 9.7-UNIT-023 — fail-closed: a PER_CHARACTER event for a model with NO
// per-1k-chars price → ErrNoPricing (never a silent zero-cost ledger row).
func TestComputeCost_PerCharacter_NoPrice_FailClosed(t *testing.T) {
	// A token model (no PricePerCharsAudio) billed PER_CHARACTER → ErrNoPricing.
	tokenSnap := NewSnapshot(map[string]Row{
		"deepseek-v3": {PriceIn: dec(t, "0.0014"), PriceOut: dec(t, "0.0028"), Markup: dec(t, "10.00")},
	})
	if _, err := tokenSnap.ComputeCost(CostInput{ModelID: "deepseek-v3", CharacterCount: 100, Mode: perChar}); err != ErrNoPricing {
		t.Fatalf("err=%v, want ErrNoPricing (fail-closed, no silent zero)", err)
	}
	// An ASR model (per-minute only, no per-chars) billed PER_CHARACTER → ErrNoPricing.
	pm := dec(t, "0.006")
	asr := NewSnapshot(map[string]Row{
		"doubao-asr": {PriceIn: decimal.Zero, PriceOut: decimal.Zero, Markup: dec(t, "10.00"), PricePerMinuteAudio: &pm},
	})
	if _, err := asr.ComputeCost(CostInput{ModelID: "doubao-asr", CharacterCount: 100, Mode: perChar}); err != ErrNoPricing {
		t.Fatalf("ASR-row PER_CHARACTER err=%v, want ErrNoPricing", err)
	}
}

// 9.7-UNIT-021 (formula half) — character_count drives cost linearly: 2000 chars
// is exactly 2× the 1000-char cost (proves the count is used verbatim; the
// rune-vs-byte computation is gateway-side). Token modes IGNORE character_count.
func TestComputeCost_PerCharacter_LinearAndIgnoredByTokenModes(t *testing.T) {
	s := ttsSnap(t, "0.015000", "10.00")
	one, _ := s.ComputeCost(CostInput{ModelID: "doubao-tts", CharacterCount: 1000, Mode: perChar})
	two, _ := s.ComputeCost(CostInput{ModelID: "doubao-tts", CharacterCount: 2000, Mode: perChar})
	if !two.Cost.Equal(one.Cost.Mul(decimal.NewFromInt(2))) {
		t.Fatalf("not linear: 1000→%s 2000→%s", one.Cost, two.Cost)
	}
	// PER_TOKEN ignores character_count entirely.
	tk := snap(t, "qwen-max", "0.0080", "0.0240", "10.00")
	a, _ := tk.ComputeCost(CostInput{ModelID: "qwen-max", PromptTokens: 1500, CompletionTokens: 800, CharacterCount: 0, Mode: perToken})
	b, _ := tk.ComputeCost(CostInput{ModelID: "qwen-max", PromptTokens: 1500, CompletionTokens: 800, CharacterCount: 9999, Mode: perToken})
	if !a.Cost.Equal(b.Cost) {
		t.Fatalf("PER_TOKEN cost changed with character_count: %s vs %s", a.Cost, b.Cost)
	}
}
