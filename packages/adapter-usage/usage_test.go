package usage

import (
	"errors"
	"testing"
)

func TestValidateInvariants_HappyPath(t *testing.T) {
	if err := ValidateInvariants(5, 10, 15); err != nil {
		t.Fatalf("ValidateInvariants(5,10,15) err = %v, want nil", err)
	}
	if err := ValidateInvariants(1, 0, 1); err != nil {
		t.Fatalf("ValidateInvariants(1,0,1) err = %v, want nil (zero completion permitted)", err)
	}
}

func TestValidateInvariants_ZeroPrompt(t *testing.T) {
	if err := ValidateInvariants(0, 5, 5); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(0,5,5) err = %v, want ErrUsageConstraintViolation", err)
	}
}

func TestValidateInvariants_NegativePrompt(t *testing.T) {
	if err := ValidateInvariants(-1, 5, 4); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(-1,5,4) err = %v, want ErrUsageConstraintViolation", err)
	}
}

func TestValidateInvariants_NegativeCompletion(t *testing.T) {
	if err := ValidateInvariants(5, -1, 4); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(5,-1,4) err = %v, want ErrUsageConstraintViolation", err)
	}
}

func TestValidateInvariants_TotalNotSum(t *testing.T) {
	if err := ValidateInvariants(5, 10, 14); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(5,10,14) err = %v, want ErrUsageConstraintViolation", err)
	}
	if err := ValidateInvariants(5, 10, 16); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(5,10,16) err = %v, want ErrUsageConstraintViolation", err)
	}
}

func TestValidateInvariants_NegativeTotal(t *testing.T) {
	if err := ValidateInvariants(5, 5, -1); !errors.Is(err, ErrUsageConstraintViolation) {
		t.Fatalf("ValidateInvariants(5,5,-1) err = %v, want ErrUsageConstraintViolation", err)
	}
}

func TestNormalisedUsage_ZeroValueIsUseful(t *testing.T) {
	var u NormalisedUsage
	if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
		t.Fatalf("zero NormalisedUsage has non-zero fields: %+v", u)
	}
}
