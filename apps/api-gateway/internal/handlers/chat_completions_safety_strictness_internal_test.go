// Story 8.4 — internal (package handlers) tests for the per-request strictness
// resolver (G3 fail-closed) + the gated non-stream redact + the 8.5
// SafetyEvent.Strictness annotation. White-box so the unexported resolver +
// redactResponse + recorder can be driven directly.
//
// Scenario IDs trace to docs/qa/assessments/8.4-test-design-20260611.md.
package handlers

import (
	"context"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// medRow is a MEDIUM-severity seed term used to differentiate loose (pass) from
// default/strict (block).
func medRow() safetylexicon.Row {
	return safetylexicon.Row{
		Lang: safetylexicon.LangEN, Category: safetylexicon.CategoryViolenceTerror,
		Severity: safetylexicon.SeverityMedium, Raw: "medoutword", File: "test/out/8.4.txt", Line: 8401,
	}
}

// ----- AC3 (G3): resolver fail-closed -----------------------------------

// 8.4-UNIT-032/033/034 + G3 — resolveStrictnessFromClaims maps nil/!ok claims and
// unknown tokens to Strict, and a valid token to its level.
func TestResolveStrictnessFromClaims_FailClosed(t *testing.T) {
	// 8.4-UNIT-033 — no CacheValue (bearer mw not run) → Strict.
	if got := resolveStrictnessFromClaims(context.Background()); got != contentsafety.Strict {
		t.Fatalf("nil claims → %q, want Strict (fail-closed)", got)
	}

	cases := []struct {
		token string
		want  contentsafety.Strictness
	}{
		{"loose", contentsafety.Loose},     // 8.4-UNIT-032 valid relax
		{"default", contentsafety.Default}, // valid relax
		{"strict", contentsafety.Strict},
		{"", contentsafety.Strict},      // 8.4-UNIT-031 pre-8.4 omitempty "" → strict
		{"off", contentsafety.Strict},   // 8.4-UNIT-034 unknown → strict
		{"high", contentsafety.Strict},  // severity token, not a level → strict
		{"Loose", contentsafety.Strict}, // mixed-case → strict
	}
	for _, c := range cases {
		ctx := middleware.WithCacheValue(context.Background(), &middleware.CachedClaims{ContentSafetyStrictness: c.token})
		if got := resolveStrictnessFromClaims(ctx); got != c.want {
			t.Fatalf("8.4: relaxed/wrong gate — claims token %q → %q, want %q", c.token, got, c.want)
		}
	}
}

// 8.4-UNIT-036 — resolve-once: resolvedStrictness returns the CARRIED value (set
// by ServeHTTP) even if it diverges from the underlying claims — proving input +
// output read the SAME once-resolved level with no mid-request re-derivation.
func TestResolvedStrictness_ResolveOnceFromCarry(t *testing.T) {
	// Underlying claims say "strict", but the carry says Loose → carry wins.
	ctx := middleware.WithCacheValue(context.Background(), &middleware.CachedClaims{ContentSafetyStrictness: "strict"})
	ctx = withResolvedStrictness(ctx, contentsafety.Loose)
	if got := resolvedStrictness(ctx); got != contentsafety.Loose {
		t.Fatalf("resolvedStrictness ignored the carry: %q want Loose", got)
	}
	// No carry → falls back to claims (still fail-closed).
	bare := middleware.WithCacheValue(context.Background(), &middleware.CachedClaims{ContentSafetyStrictness: "off"})
	if got := resolvedStrictness(bare); got != contentsafety.Strict {
		t.Fatalf("resolvedStrictness fallback wrong: %q want Strict", got)
	}
}

// ----- AC2 (output non-stream): redact gated by the resolved level -------

// 8.4-UNIT-018 — non-stream redact honours the per-request level: a MEDIUM output
// term is redacted under default but PASSES under loose. strict redacts it too.
func TestRedactResponse_GatedByLevel(t *testing.T) {
	rec := &outRecorder{}
	h := seededOutputHandler(t, rec, medRow())

	// loose (min=High): a medium term is below threshold → NOT redacted.
	looseCtx := withResolvedStrictness(outputCtx(), contentsafety.Loose)
	resp := oneChoiceResp("model said medoutword in passing")
	if _, hit := h.redactResponse(looseCtx, resp); hit {
		t.Fatal("loose must NOT redact a medium term")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("loose path mutated finish_reason: %q", resp.Choices[0].FinishReason)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("loose recorded %d events, want 0 (sub-threshold → no block)", len(rec.all()))
	}

	// default (min=Medium): the SAME term meets the threshold → redacted.
	defCtx := withResolvedStrictness(outputCtx(), contentsafety.Default)
	resp2 := oneChoiceResp("model said medoutword in passing")
	if _, hit := h.redactResponse(defCtx, resp2); !hit {
		t.Fatal("default must redact a medium term")
	}
	if resp2.Choices[0].FinishReason != "content_filter" {
		t.Fatalf("default redact finish_reason = %q, want content_filter", resp2.Choices[0].FinishReason)
	}
}

// ----- AC4: 8.5 SafetyEvent.Strictness (OQ-8.4-5) -----------------------

// 8.4-UNIT-045 — the OUTPUT event is recorded ONLY on a gated-block and carries
// the effective level; a sub-threshold detected-but-not-blocked match records
// NOTHING. 8.4-UNIT-046 — the no-op default still persists nothing.
func TestSafetyEvent_StrictnessAnnotation(t *testing.T) {
	rec := &outRecorder{}
	h := seededOutputHandler(t, rec, medRow())

	// Gated-block under default → exactly one event carrying Strictness="default".
	defCtx := withResolvedStrictness(outputCtx(), contentsafety.Default)
	h.redactResponse(defCtx, oneChoiceResp("emits medoutword here"))
	evs := rec.all()
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want 1 on a gated-block", len(evs))
	}
	if evs[0].Strictness != "default" {
		t.Fatalf("SafetyEvent.Strictness = %q, want default (effective level)", evs[0].Strictness)
	}

	// Sub-threshold under loose → NO event (the match was detected but not blocked).
	rec2 := &outRecorder{}
	h2 := seededOutputHandler(t, rec2, medRow())
	looseCtx := withResolvedStrictness(outputCtx(), contentsafety.Loose)
	h2.redactResponse(looseCtx, oneChoiceResp("emits medoutword here"))
	if n := len(rec2.all()); n != 0 {
		t.Fatalf("sub-threshold recorded %d events, want 0 (log-only-on-block, BR-4.4)", n)
	}
}
