// Story 8.4 — CachedClaims.ContentSafetyStrictness JSON shape + the omitempty
// rollout backward-compat (8.4-UNIT-030/031). The omitempty tag IS the
// fail-closed mechanism: a pre-8.4 Redis entry (no key) deserialises "" which the
// gateway resolver maps to strict.
package middleware_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// 8.4-UNIT-030 — a populated level round-trips through the Redis JSON cache: the
// key is present on marshal and restored on unmarshal.
func TestCachedClaims_StrictnessRoundTrip(t *testing.T) {
	in := middleware.CachedClaims{APIKeyID: "k", UserID: "u", ContentSafetyStrictness: "loose"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content_safety_strictness":"loose"`) {
		t.Fatalf("marshaled JSON missing the strictness key: %s", b)
	}
	var out middleware.CachedClaims
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.ContentSafetyStrictness != "loose" {
		t.Fatalf("round-trip lost strictness: %q", out.ContentSafetyStrictness)
	}
}

// 8.4-UNIT-031 — omitempty rollout: a pre-8.4 cached entry (JSON WITHOUT the key)
// deserialises with the field "" — which the gateway resolver fail-closes to
// strict. Also: an empty value is OMITTED on marshal (the omitempty contract).
func TestCachedClaims_StrictnessOmitemptyRollout(t *testing.T) {
	// Pre-8.4 Redis JSON: no content_safety_strictness key at all.
	pre84 := `{"api_key_id":"k","user_id":"u","team_id":"","scope":"{}"}`
	var out middleware.CachedClaims
	if err := json.Unmarshal([]byte(pre84), &out); err != nil {
		t.Fatal(err)
	}
	if out.ContentSafetyStrictness != "" {
		t.Fatalf("pre-8.4 entry deserialised strictness=%q, want \"\" (→ resolver fail-closes to strict)", out.ContentSafetyStrictness)
	}

	// An empty field is omitted on marshal (omitempty), keeping old/new shapes
	// interchangeable during the rollout window.
	b, _ := json.Marshal(middleware.CachedClaims{APIKeyID: "k"})
	if strings.Contains(string(b), "content_safety_strictness") {
		t.Fatalf("empty strictness should be omitted (omitempty), got: %s", b)
	}
}
