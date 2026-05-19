package usage

import (
	"errors"
	"testing"

	"github.com/he-api/he-api/apps/adapters/kimi/internal/upstream"
	sharedusage "github.com/he-api/he-api/packages/adapter-usage"
)

// 4.3-UNIT-006 (P0) — BR-3.2 Moonshot identity mapping using lifted
// shared types from packages/adapter-usage (OQ-4.2-3a anchor).
func TestNormalise_Kimi_CompatMode_Identity(t *testing.T) {
	n := NewKimi()
	got, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.PromptTokens != 5 || got.CompletionTokens != 10 || got.TotalTokens != 15 {
		t.Fatalf("identity mapping mismatch: %#v", got)
	}
}

// 4.3-UNIT-006/lifted_import_path — verifies the Normaliser's
// NormalisedUsage type is the SAME underlying type as the shared lift
// (OQ-4.2-3a anchor test).
func TestNormalise_Kimi_NormalisedUsage_IsLiftedType(t *testing.T) {
	var _ sharedusage.NormalisedUsage = NormalisedUsage{} // assignable both ways
	var _ NormalisedUsage = sharedusage.NormalisedUsage{}
}

// 4.3-UNIT-006a (P0) — BR-3.3 positive-prompt invariant.
func TestNormalise_Kimi_ZeroPrompt_IsConstraintViolation(t *testing.T) {
	n := NewKimi()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 0, CompletionTokens: 5, TotalTokens: 5})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.3-UNIT-006b (P0) — BR-3.3 non-negative completion invariant.
func TestNormalise_Kimi_NegativeCompletion_IsConstraintViolation(t *testing.T) {
	n := NewKimi()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: -1, TotalTokens: 4})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.3-UNIT-006c (P0) — BR-3.3 exact-sum invariant.
func TestNormalise_Kimi_TotalNotSum_IsConstraintViolation(t *testing.T) {
	n := NewKimi()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 14})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.3-UNIT-007 (P0, pre-skipped pending native-endpoint ratification —
// OQ-4.2-1 ratified Moonshot, so native field-rename mapping is NOT
// exercised. Stub kept for Stories 4.3-4.6 inheritance.)
func TestNormalise_Kimi_NativeFieldRename_Skipped(t *testing.T) {
	t.Skip("OQ-4.2-1 ratified Moonshot; native field-rename Normaliser path is NOT exercised in Story 4.2")
}

// 4.3-UNIT-006-zero-completion-permitted — BR-3.3 explicitly allows zero
// completion_tokens (refusal / max_tokens=0 short-circuit).
func TestNormalise_Kimi_ZeroCompletion_Permitted(t *testing.T) {
	n := NewKimi()
	got, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 0, TotalTokens: 5})
	if err != nil {
		t.Fatalf("zero completion must be permitted, got err = %v", err)
	}
	if got.CompletionTokens != 0 || got.TotalTokens != 5 {
		t.Fatalf("zero-completion mapping mismatch: %#v", got)
	}
}
