package usage

import (
	"errors"
	"testing"

	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
)

// 4.1-UNIT-034 (P0) — BR-3.2 identity-mapping.
func TestNormalise_DeepSeek_Identity(t *testing.T) {
	n := NewDeepSeek()
	got, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.PromptTokens != 5 || got.CompletionTokens != 10 || got.TotalTokens != 15 {
		t.Fatalf("identity mapping mismatch: %#v", got)
	}
}

// 4.1-UNIT-035 (P0) — BR-3.3 positive prompt_tokens invariant.
func TestNormalise_ZeroPrompt_IsError(t *testing.T) {
	n := NewDeepSeek()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 0, CompletionTokens: 5, TotalTokens: 5})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.1-UNIT-036 (P0) — BR-3.3 non-negative completion_tokens invariant.
func TestNormalise_NegativeCompletion_IsError(t *testing.T) {
	n := NewDeepSeek()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: -1, TotalTokens: 4})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.1-UNIT-037 (P0) — BR-3.3 exact-sum invariant (no derived-field drift).
func TestNormalise_TotalNotPromptPlusCompletion_IsError(t *testing.T) {
	n := NewDeepSeek()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 14})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.1-UNIT-038 (P0) — BR-3.3 non-negative total_tokens invariant.
func TestNormalise_NegativeTotal_IsError(t *testing.T) {
	n := NewDeepSeek()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: -5})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.1-UNIT-039 (P0) — BR-3.4 missing-usage hard failure (raw value all-zero
// is the synthetic "no usage emitted" state; the adapter's missing-usage
// check happens at the JSON-decode boundary, NOT in the Normaliser. This
// test pins the Normaliser's behaviour against the zero-RawUsage to ensure
// the BR-3.3 invariant catches it).
func TestNormalise_AllZero_IsConstraintViolation(t *testing.T) {
	n := NewDeepSeek()
	_, err := n.Normalise(upstream.RawUsage{})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation (PromptTokens=0)", err)
	}
}
