// Story 6.4 AC1 — X-He-AB-Models header parsing (Q-H exactly-2-distinct).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-001  "a,b"        -> [a,b] exactly 2 distinct
//	6.4-UNIT-003  "a,a"        -> dedup -> 1 -> error (BR1-4 dedup-before-count)
//	6.4-UNIT-005  "a"          -> error (count != 2)
//	6.4-UNIT-006  "a,b,c"      -> error (count != 2)
//	            blank/absent   -> (nil, nil) — NOT an A/B request (non-A/B passthrough)
package routingclient_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
)

func hdrWithAB(v string) http.Header {
	h := http.Header{}
	if v != "" {
		h.Set(routingclient.ABModelsHeader, v)
	}
	return h
}

// 6.4-UNIT-001 (P0) — two distinct ids parse to exactly [a,b] in order.
func TestParseABModels_TwoDistinct(t *testing.T) {
	got, err := routingclient.ParseABModels(hdrWithAB("qwen-max,deepseek-v3"))
	if err != nil {
		t.Fatalf("ParseABModels err = %v, want nil", err)
	}
	if len(got) != 2 || got[0] != "qwen-max" || got[1] != "deepseek-v3" {
		t.Fatalf("ParseABModels = %v, want [qwen-max deepseek-v3]", got)
	}
}

// 6.4-UNIT-001b — surrounding whitespace per id is trimmed.
func TestParseABModels_TrimsWhitespace(t *testing.T) {
	got, err := routingclient.ParseABModels(hdrWithAB("  qwen-max , deepseek-v3 "))
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 2 || got[0] != "qwen-max" || got[1] != "deepseek-v3" {
		t.Fatalf("ParseABModels = %v, want [qwen-max deepseek-v3]", got)
	}
}

// 6.4-UNIT-003 (P0) — duplicate ids de-dup to 1 -> error (dedup BEFORE count).
func TestParseABModels_DuplicateRejected(t *testing.T) {
	if _, err := routingclient.ParseABModels(hdrWithAB("qwen-max,qwen-max")); err == nil {
		t.Fatal("ParseABModels(a,a) err = nil, want error (dedup -> 1 -> 400)")
	}
}

// 6.4-UNIT-005 (P0) — a single id -> error (count != 2).
func TestParseABModels_SingleRejected(t *testing.T) {
	if _, err := routingclient.ParseABModels(hdrWithAB("qwen-max")); err == nil {
		t.Fatal("ParseABModels(a) err = nil, want error")
	}
}

// 6.4-UNIT-006 (P0) — three ids -> error (count != 2).
func TestParseABModels_ThreeRejected(t *testing.T) {
	if _, err := routingclient.ParseABModels(hdrWithAB("a,b,c")); err == nil {
		t.Fatal("ParseABModels(a,b,c) err = nil, want error")
	}
}

// blank/absent header is NOT an A/B request: (nil, nil), no error — the
// non-A/B path proceeds unchanged (zero regression).
func TestParseABModels_AbsentIsNotAB(t *testing.T) {
	got, err := routingclient.ParseABModels(http.Header{})
	if err != nil || got != nil {
		t.Fatalf("ParseABModels(absent) = (%v, %v), want (nil, nil)", got, err)
	}
	got, err = routingclient.ParseABModels(hdrWithAB("   "))
	if err != nil || got != nil {
		t.Fatalf("ParseABModels(blank) = (%v, %v), want (nil, nil)", got, err)
	}
}

// ABModelsPresent reflects raw presence (non-blank), independent of validity —
// the streaming guard (BR4-2) keys off presence, not the exactly-2 check.
func TestABModelsPresent(t *testing.T) {
	if !routingclient.ABModelsPresent(hdrWithAB("a")) {
		t.Error("ABModelsPresent(a) = false, want true (1 id is still present)")
	}
	if routingclient.ABModelsPresent(hdrWithAB("  ")) {
		t.Error("ABModelsPresent(blank) = true, want false")
	}
	if routingclient.ABModelsPresent(http.Header{}) {
		t.Error("ABModelsPresent(absent) = true, want false")
	}
}

// AC4/BR4-4 — RecordABOutcome forwards the bounded-cardinality outcome to the
// he_routing_ab_total instrument for all 3 labels, and is a safe no-op on a nil
// Decider (routing disabled). Exercises the thin forwarder the handler drives
// through h.router (cross-package, so uncounted in this package's own coverage).
func TestRecordABOutcome(t *testing.T) {
	d := routingclient.NewDecider(nil, nil) // non-nil Decider with real metrics
	for _, outcome := range []string{
		routingclient.ABOutcomeBothOK,
		routingclient.ABOutcomePartial,
		routingclient.ABOutcomeBothFailed,
	} {
		d.RecordABOutcome(context.Background(), outcome) // must not panic
	}

	// nil receiver — the handler guard (Safe on a nil Decider) is a no-op.
	var nilD *routingclient.Decider
	nilD.RecordABOutcome(context.Background(), routingclient.ABOutcomeBothOK)
}
