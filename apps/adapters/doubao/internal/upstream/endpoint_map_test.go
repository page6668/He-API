package upstream

import (
	"errors"
	"testing"
)

// 4.5-UNIT-003 (P0) — BR-1.12 fail-fast: unmapped model id returns
// ErrUnsupportedModel; the adapter MUST NOT proceed with the upstream
// call. Uses the test-injectable New(envProvider) constructor per
// Architect Round 1 m-1 refactor.
func TestEndpointMap_Lookup_UnmappedModel_FailFast(t *testing.T) {
	em := New(func(string) string { return "ep-test-real" })
	got, err := em.Lookup("unknown-id")
	if got != "" {
		t.Fatalf("Lookup(unknown-id) returned %q, want empty string", got)
	}
	if !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("Lookup(unknown-id) err = %v, want ErrUnsupportedModel", err)
	}
}

// 4.5-UNIT-004 (P0) — BR-1.12 fail-fast: env-var unset/empty → Lookup
// returns ErrUnsupportedModel. Verifies the env-var-empty branch is
// DISTINCT from the unmapped-id branch (4.5-UNIT-003). Architect m-1
// refactor anchor: the test-injectable constructor lets us flip the env
// without TestMain gymnastics.
func TestEndpointMap_Lookup_EnvVarUnset_FailFast(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	em := New(emptyEnv)
	if _, err := em.Lookup(ModelDoubaoPro); !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("Lookup(doubao-pro) with empty DOUBAO_PRO_ENDPOINT_ID err = %v, want ErrUnsupportedModel", err)
	}
	if _, err := em.Lookup(ModelDoubaoLite); !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("Lookup(doubao-lite) with empty DOUBAO_LITE_ENDPOINT_ID err = %v, want ErrUnsupportedModel", err)
	}
}

// TestEndpointMap_Lookup_HappyPath — both friendly ids resolve to the
// expected endpoint id when env vars are set.
func TestEndpointMap_Lookup_HappyPath(t *testing.T) {
	em := New(func(name string) string {
		switch name {
		case EnvDoubaoProEndpointID:
			return "ep-test-pro-001"
		case EnvDoubaoLiteEndpointID:
			return "ep-test-lite-001"
		}
		return ""
	})
	if got, err := em.Lookup(ModelDoubaoPro); err != nil || got != "ep-test-pro-001" {
		t.Fatalf("Lookup(doubao-pro) = (%q, %v), want (ep-test-pro-001, nil)", got, err)
	}
	if got, err := em.Lookup(ModelDoubaoLite); err != nil || got != "ep-test-lite-001" {
		t.Fatalf("Lookup(doubao-lite) = (%q, %v), want (ep-test-lite-001, nil)", got, err)
	}
}

// TestEndpointMap_New_NilEnvProvider_DefaultsToOsGetenv — defensive
// behaviour: a nil EnvProvider falls back to os.Getenv (covers the
// startup path that uses NewFromOS()).
func TestEndpointMap_New_NilEnvProvider_UsesOsGetenv(t *testing.T) {
	t.Setenv(EnvDoubaoProEndpointID, "ep-from-os-pro")
	em := New(nil)
	if got, err := em.Lookup(ModelDoubaoPro); err != nil || got != "ep-from-os-pro" {
		t.Fatalf("Lookup(doubao-pro) via os.Getenv = (%q, %v), want (ep-from-os-pro, nil)", got, err)
	}
}

// TestEndpointMap_Constants_LiteralStrings — anchors OQ-4.5-2 brand-prefix
// + OQ-4.5-3 friendly-id literal values. Locks the env-var names that
// the Helm ConfigMap and dev seed script use.
func TestEndpointMap_Constants_LiteralStrings(t *testing.T) {
	cases := map[string]string{
		"ModelDoubaoPro":          ModelDoubaoPro,
		"ModelDoubaoLite":         ModelDoubaoLite,
		"EnvDoubaoProEndpointID":  EnvDoubaoProEndpointID,
		"EnvDoubaoLiteEndpointID": EnvDoubaoLiteEndpointID,
	}
	wants := map[string]string{
		"ModelDoubaoPro":          "doubao-pro",
		"ModelDoubaoLite":         "doubao-lite",
		"EnvDoubaoProEndpointID":  "DOUBAO_PRO_ENDPOINT_ID",
		"EnvDoubaoLiteEndpointID": "DOUBAO_LITE_ENDPOINT_ID",
	}
	for name, got := range cases {
		if got != wants[name] {
			t.Fatalf("%s = %q, want %q (OQ-4.5-2 brand-prefix lock-in)", name, got, wants[name])
		}
	}
}
