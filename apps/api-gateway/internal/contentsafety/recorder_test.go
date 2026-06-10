// Story 8.2 — Recorder / SafetyEvent seam tests (AC3, zero-DB). The handler-built
// event-shape assertions (UNIT-020/021/022) live in the handler test (the handler
// constructs the event); these tests cover the column-shape parity contract and
// the no-op default in isolation.
//
// Scenario IDs trace to docs/qa/assessments/8.2-test-design-20260610.md.
package contentsafety

import (
	"context"
	"testing"
	"unicode/utf8"
)

// content_safety_logs column widths (docs/architecture/data-models.md:142-154) —
// the Story-8.5 table the SafetyEvent must fit byte-for-byte (BR-3.2).
const (
	colDirectionMax   = 10  // direction VARCHAR(10)
	colMatchedRuleMax = 100 // matched_rule VARCHAR(100)
	colActionMax      = 20  // action VARCHAR(20)
)

// 8.2-UNIT-023 / 8.2-BLIND-DATA-001 — the literals Story 8.2 emits and any
// well-formed SafetyEvent fit the content_safety_logs column widths, so Story 8.5
// binds its persisting Recorder with no contract change.
func TestRecorder_UNIT023_DATA001_ColumnShapeParity(t *testing.T) {
	if n := utf8.RuneCountInString(DirectionInput); n > colDirectionMax {
		t.Fatalf("DirectionInput %q = %d runes, exceeds direction VARCHAR(%d)", DirectionInput, n, colDirectionMax)
	}
	if n := utf8.RuneCountInString(ActionBlocked); n > colActionMax {
		t.Fatalf("ActionBlocked %q = %d runes, exceeds action VARCHAR(%d)", ActionBlocked, n, colActionMax)
	}
	// matched_rule == Match.Canonical is capped ≤100 runes by 8.1; assert the
	// event field carrying it respects the same ceiling for a representative event.
	ev := SafetyEvent{
		Direction:   DirectionInput,
		MatchedRule: "badword",
		Category:    "abuse",
		Severity:    "high",
		Action:      ActionBlocked,
		UserID:      "u-1",
		APIKeyID:    "k-1",
		HeRequestID: "req_abc",
	}
	if n := utf8.RuneCountInString(ev.MatchedRule); n > colMatchedRuleMax {
		t.Fatalf("matched_rule = %d runes, exceeds VARCHAR(%d)", n, colMatchedRuleMax)
	}
	if ev.Direction != "input" || ev.Action != "blocked" {
		t.Fatalf("event literals drifted: direction=%q action=%q", ev.Direction, ev.Action)
	}
}

// 8.2-UNIT-022 (recorder half) — the NopRecorder persists nothing and never
// panics (zero-DB this story; the persisting impl is Story 8.5). The handler half
// of UNIT-022 (request still 400s with the no-op default) is in the handler test.
func TestRecorder_UNIT022_NopRecorderNoPanicNoState(t *testing.T) {
	var r Recorder = NopRecorder{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("NopRecorder.Record panicked: %v", rec)
		}
	}()
	r.Record(context.Background(), SafetyEvent{Direction: DirectionInput, Action: ActionBlocked, MatchedRule: "x"})
	// A second call with a zero-value event is equally safe.
	r.Record(context.Background(), SafetyEvent{})
}
