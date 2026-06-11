// Story 9.1 AC1 — row mapping unit tests (9.1-UNIT-010 named-column INSERT,
// 9.1-UNIT-011 field clamps). Driver-free: no live ClickHouse needed.
package clickhouse

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func goodEvent() *analyticsv1.UsageLogEvent {
	return &analyticsv1.UsageLogEvent{
		HeRequestId:        "req_abcdef012345",
		UserId:             "11111111-1111-1111-1111-111111111111",
		ApiKeyId:           "22222222-2222-2222-2222-222222222222",
		Model:              "qwen-max",
		UpstreamModel:      "qwen-max-2025",
		RoutingStrategy:    "latency",
		SelectedByStrategy: "qwen-max-2025",
		StatusCode:         200,
		PromptTokens:       100,
		CompletionTokens:   200,
		TotalTokens:        300,
		CostUsd:            "0.001234",
		IsStreaming:        true,
		ClientIp:           "203.0.113.7",
		ClientCountry:      "SG",
		Ts:                 "2026-06-11T12:34:56Z",
	}
}

// 9.1-UNIT-010 — the INSERT is named-column: selected_by_strategy is written;
// team_id / user_agent / error_message are OMITTED (left to CH column defaults).
func TestInsertStatementNamedColumns(t *testing.T) {
	if !strings.Contains(InsertStatement, "selected_by_strategy") {
		t.Fatalf("INSERT must write selected_by_strategy (H-3): %s", InsertStatement)
	}
	for _, omitted := range []string{"team_id", "user_agent", "error_message"} {
		if strings.Contains(InsertStatement, omitted) {
			t.Fatalf("INSERT must OMIT %s (column default): %s", omitted, InsertStatement)
		}
	}
	if got := len(insertColumns); got != 21 {
		t.Fatalf("expected 21 named columns, got %d", got)
	}
	// appendArgs must line up 1:1 with the column list.
	if got := len(Row{}.appendArgs()); got != len(insertColumns) {
		t.Fatalf("appendArgs (%d) != columns (%d)", got, len(insertColumns))
	}
}

func TestEventToRowMapsServedFacts(t *testing.T) {
	row, err := EventToRow(goodEvent(), discard())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if row.SelectedByStrategy != "qwen-max-2025" || row.StatusCode != 200 || row.TotalTokens != 300 {
		t.Fatalf("mapping mismatch: %+v", row)
	}
	if row.CostUSD.String() != "0.001234" {
		t.Fatalf("cost_usd = %s, want 0.001234", row.CostUSD.String())
	}
	if row.IsStreaming != 1 {
		t.Fatalf("is_streaming = %d, want 1", row.IsStreaming)
	}
	if row.ClientIP.String() != "203.0.113.7" || row.ClientCountry != "SG" {
		t.Fatalf("ip/country mismatch: %s / %q", row.ClientIP, row.ClientCountry)
	}
	if row.Ts.IsZero() {
		t.Fatal("ts should parse")
	}
}

// 9.1-UNIT-011 — clamps: out-of-range status → 0; negative/garbage cost → 0;
// non-v4/empty ip → 0.0.0.0; odd country normalised to 2 bytes; bad ts → now.
func TestEventToRowClamps(t *testing.T) {
	ev := goodEvent()
	ev.StatusCode = 9999    // out of [100,599]
	ev.CostUsd = "-5.00"    // negative → 0
	ev.ClientIp = "not-an-ip"
	ev.ClientCountry = "SGP" // 3 chars → 2
	ev.Ts = "garbage"

	row, err := EventToRow(ev, discard())
	if err != nil {
		t.Fatalf("clamps must not error: %v", err)
	}
	if row.StatusCode != 0 {
		t.Fatalf("status clamp: got %d want 0", row.StatusCode)
	}
	if !row.CostUSD.IsZero() {
		t.Fatalf("negative cost clamp: got %s want 0", row.CostUSD)
	}
	if row.ClientIP.String() != "0.0.0.0" {
		t.Fatalf("ip clamp: got %s", row.ClientIP)
	}
	if row.ClientCountry != "SG" {
		t.Fatalf("country clamp: got %q want SG", row.ClientCountry)
	}
	if row.Ts.IsZero() {
		t.Fatal("bad ts should default to now, not zero")
	}

	// empty cost → 0 (NON-NULL default, H-1); 1-char country → padded.
	ev2 := goodEvent()
	ev2.CostUsd = ""
	ev2.ClientCountry = "S"
	row2, _ := EventToRow(ev2, discard())
	if !row2.CostUSD.IsZero() {
		t.Fatalf("empty cost should map to 0, got %s", row2.CostUSD)
	}
	if row2.ClientCountry != "S " {
		t.Fatalf("1-char country pad: got %q want %q", row2.ClientCountry, "S ")
	}
}

// Malformed events (bad he_request_id / empty user_id) → ErrMalformed → DLQ.
func TestEventToRowMalformed(t *testing.T) {
	bad := goodEvent()
	bad.HeRequestId = "not-a-req-id"
	if _, err := EventToRow(bad, discard()); err != ErrMalformed {
		t.Fatalf("bad he_request_id: want ErrMalformed, got %v", err)
	}

	noUser := goodEvent()
	noUser.UserId = ""
	if _, err := EventToRow(noUser, discard()); err != ErrMalformed {
		t.Fatalf("empty user_id: want ErrMalformed, got %v", err)
	}
}
