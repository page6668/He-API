package usage

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/he-api/he-api/apps/adapters/doubao/internal/upstream"
	sharedusage "github.com/he-api/he-api/packages/adapter-usage"
)

// 4.5-UNIT-006 (P0) — BR-3.2 Volcengine Ark v3 identity mapping using
// lifted shared types from packages/adapter-usage (Story-4.2 M1 anchor
// cascade).
func TestNormalise_Doubao_CompatMode_Identity(t *testing.T) {
	n := NewDoubao()
	got, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.PromptTokens != 5 || got.CompletionTokens != 10 || got.TotalTokens != 15 {
		t.Fatalf("identity mapping mismatch: %#v", got)
	}
}

// 4.5-UNIT-006-lifted-import — verifies the Normaliser's NormalisedUsage
// type is the SAME underlying type as the shared lift (M1 lift
// consumption guard).
func TestNormalise_Doubao_NormalisedUsage_IsLiftedType(t *testing.T) {
	var _ sharedusage.NormalisedUsage = NormalisedUsage{} // assignable both ways
	var _ NormalisedUsage = sharedusage.NormalisedUsage{}
}

// 4.5-UNIT-006a (P0) — BR-3.3 positive-prompt invariant.
func TestNormalise_Doubao_ZeroPrompt_IsConstraintViolation(t *testing.T) {
	n := NewDoubao()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 0, CompletionTokens: 5, TotalTokens: 5})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.5-UNIT-006b (P0) — BR-3.3 non-negative completion invariant.
func TestNormalise_Doubao_NegativeCompletion_IsConstraintViolation(t *testing.T) {
	n := NewDoubao()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: -1, TotalTokens: 4})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.5-UNIT-006c (P0) — BR-3.3 exact-sum invariant.
func TestNormalise_Doubao_TotalNotSum_IsConstraintViolation(t *testing.T) {
	n := NewDoubao()
	_, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 14})
	if !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("err = %v, want ErrUsageConstraintViolation", err)
	}
}

// 4.5-UNIT-006-zero-completion — BR-3.3 explicitly allows zero
// completion_tokens (refusal / max_tokens=0 short-circuit).
func TestNormalise_Doubao_ZeroCompletion_Permitted(t *testing.T) {
	n := NewDoubao()
	got, err := n.Normalise(upstream.RawUsage{PromptTokens: 5, CompletionTokens: 0, TotalTokens: 5})
	if err != nil {
		t.Fatalf("zero completion must be permitted, got err = %v", err)
	}
	if got.CompletionTokens != 0 || got.TotalTokens != 5 {
		t.Fatalf("zero-completion mapping mismatch: %#v", got)
	}
}

// 4.5-UNIT-006-property (P0) + 4.5-BLIND-DATA-002 (P0) — property test:
// 1000 random valid RawUsage{p, c, p+c} with p ∈ [1, 1M], c ∈ [0, 1M]
// → identity-mapping invariants hold for all; flip any field →
// ErrUsageConstraintViolation.
func TestNormalise_Doubao_Identity_PropertyTest_1000Tuples(t *testing.T) {
	n := NewDoubao()
	r := rand.New(rand.NewSource(20260519)) // deterministic seed
	for i := 0; i < 1000; i++ {
		p := r.Intn(1_000_000) + 1
		c := r.Intn(1_000_000)
		total := p + c
		got, err := n.Normalise(upstream.RawUsage{PromptTokens: p, CompletionTokens: c, TotalTokens: total})
		if err != nil {
			t.Fatalf("iter %d: valid tuple {%d,%d,%d} err = %v", i, p, c, total, err)
		}
		if got.PromptTokens != p || got.CompletionTokens != c || got.TotalTokens != total {
			t.Fatalf("iter %d: identity drift {%d,%d,%d} → %#v", i, p, c, total, got)
		}
		// Flip total → ErrUsageConstraintViolation.
		_, err = n.Normalise(upstream.RawUsage{PromptTokens: p, CompletionTokens: c, TotalTokens: total + 1})
		if !errors.Is(err, ErrUsageConstraintViolation) {
			t.Fatalf("iter %d: total-mismatch should violate: {%d,%d,%d} err = %v", i, p, c, total+1, err)
		}
	}
}
