// Story 9.3 AC2 — request_logs export dumper tests (security-critical P0s).
//
//   - 9.3-UNIT-020 [P0] PII+cost exclusion: LogRow / projection has EXACTLY the
//     13 BR-RD-9 columns; no client_ip/country/user_agent/error_message/cost_usd
//   - 9.3-UNIT-022 [P0] CSV OWASP formula-injection escape (= + - @ \t \r)
//   - 9.3-UNIT-023 [P0] RFC-4180 quoting (comma, internal quote-doubling, CRLF)
//   - 9.3-UNIT-026     JSON-Lines (one object per line)
//   - 9.3-UNIT-033     unknown format → error (don't guess)
//   - 9.3-BLIND-BOUNDARY-005 zero rows → valid header-only CSV / empty JSONL
//   - column drift-guard vs 9.2 logsSelectColumns (Architect Q-COLS / Low-3)
package dumps_test

import (
	"bufio"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/analytics-svc/internal/dumps"
)

type fakeFetcher struct {
	rows    []dumps.LogRow
	gotUser string
}

func (f *fakeFetcher) FetchLogRows(_ context.Context, userID string, _, _ time.Time) ([]dumps.LogRow, error) {
	f.gotUser = userID
	return f.rows, nil
}

func sampleRow() dumps.LogRow {
	return dumps.LogRow{
		HeRequestID: "req_abcdef012345", Ts: time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		Model: "qwen-max", UpstreamModel: "qwen-max-2025", StatusCode: 200, IsStreaming: true,
		PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300, LatencyMsTotal: 420, TtfbMs: 95,
		ApiKeyID: "22222222-2222-2222-2222-222222222222", ErrorCode: "",
	}
}

// 9.3-UNIT-020 [P0]: the exported column set is EXACTLY the 13 BR-RD-9 columns —
// no PII (client_ip/country/user_agent/error_message), no cost_usd. Also pins
// the LogRow struct fields to the same set (drift guard, Architect Q-COLS).
func TestExportColumns_MatchBRRD9(t *testing.T) {
	t.Parallel()
	want := []string{
		"he_request_id", "ts", "model", "upstream_model", "status_code",
		"is_streaming", "prompt_tokens", "completion_tokens", "total_tokens",
		"latency_ms_total", "ttfb_ms", "api_key_id", "error_code",
	}
	if !reflect.DeepEqual(dumps.ExportColumns, want) {
		t.Fatalf("ExportColumns drifted from BR-RD-9:\n got=%v\nwant=%v", dumps.ExportColumns, want)
	}

	// The LogRow JSON tags must equal the same set (no extra PII/cost field).
	got := jsonTags(dumps.LogRow{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LogRow json tags drifted:\n got=%v\nwant=%v", got, want)
	}
	for _, banned := range []string{"client_ip", "client_country", "user_agent", "error_message", "cost_usd"} {
		for _, c := range got {
			if c == banned {
				t.Fatalf("BANNED PII/cost column present in LogRow: %s", banned)
			}
		}
	}

	// The SELECT projection must reference the same set + the 9.2 toString cast.
	if !strings.Contains(dumps.LogRowSelectColumns, "toString(api_key_id) AS api_key_id") {
		t.Errorf("LogRowSelectColumns missing the 9.2 api_key_id cast")
	}
	for _, banned := range []string{"client_ip", "user_agent", "error_message", "cost_usd"} {
		if strings.Contains(dumps.LogRowSelectColumns, banned) {
			t.Fatalf("BANNED column in SELECT projection: %s", banned)
		}
	}
}

func jsonTags(v any) []string {
	rt := reflect.TypeOf(v)
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if comma := strings.Index(tag, ","); comma >= 0 {
			tag = tag[:comma]
		}
		out = append(out, tag)
	}
	return out
}

// 9.3-UNIT-022 [P0]: CSV formula-injection escape.
func TestCSV_FormulaInjectionEscaped(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{"=cmd()", "+1+1", "-2+3", "@SUM(A1)", "\tno", "\rno"} {
		row := sampleRow()
		row.Model = payload
		out := dumpCSV(t, []dumps.LogRow{row})
		dataLine := csvDataLines(out)[0]
		// the model cell is column index 2 (he_request_id, ts, model, ...)
		cell := splitCSVLine(dataLine)[2]
		// Cells containing CR/comma/quote are additionally RFC-4180 quote-wrapped,
		// so the neutralizing `'` may sit just inside an opening double-quote.
		if !strings.HasPrefix(cell, "'") && !strings.HasPrefix(cell, `"'`) {
			t.Errorf("formula payload %q not neutralized: cell=%q", payload, cell)
		}
	}
}

// 9.3-UNIT-023 [P0]: RFC-4180 quoting + CRLF.
func TestCSV_RFC4180Quoting(t *testing.T) {
	t.Parallel()
	row := sampleRow()
	row.Model = `a,b"c`        // comma + internal quote
	row.UpstreamModel = "x\ny" // embedded newline
	out := dumpCSV(t, []dumps.LogRow{row})

	if !strings.HasPrefix(out, "he_request_id,ts,model,") {
		t.Errorf("missing/!ordered header: %q", firstLine(out))
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("CSV must use CRLF terminators")
	}
	if !strings.Contains(out, `"a,b""c"`) {
		t.Errorf("comma+quote field not RFC-4180 quoted: %s", out)
	}
	if !strings.Contains(out, "\"x\ny\"") {
		t.Errorf("newline field not quoted: %q", out)
	}
}

// 9.3-UNIT-026: JSON-Lines — one JSON object per line, decodable, no PII keys.
func TestJSONL_OneObjectPerLine(t *testing.T) {
	t.Parallel()
	rows := []dumps.LogRow{sampleRow(), sampleRow()}
	var sb strings.Builder
	n, err := dumps.NewRequestLogsExportDumper(&fakeFetcher{rows: rows}).
		Dump(context.Background(), "u-1", "json", time.Time{}, time.Time{}, &sb)
	if err != nil || n != 2 {
		t.Fatalf("dump json: n=%d err=%v", n, err)
	}
	sc := bufio.NewScanner(strings.NewReader(sb.String()))
	count := 0
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v", count, err)
		}
		if _, bad := m["cost_usd"]; bad {
			t.Fatalf("cost_usd leaked into JSONL")
		}
		if _, bad := m["client_ip"]; bad {
			t.Fatalf("client_ip leaked into JSONL")
		}
		count++
	}
	if count != 2 {
		t.Fatalf("want 2 JSONL lines, got %d", count)
	}
}

// 9.3-UNIT-021 [P0]: the dumper passes the caller's user_id straight to the
// fetcher (the fetcher then fences WHERE user_id = ?).
func TestDump_PassesUserIDToFetcher(t *testing.T) {
	t.Parallel()
	f := &fakeFetcher{rows: []dumps.LogRow{sampleRow()}}
	var sb strings.Builder
	if _, err := dumps.NewRequestLogsExportDumper(f).Dump(context.Background(), "user-A", "json", time.Time{}, time.Time{}, &sb); err != nil {
		t.Fatal(err)
	}
	if f.gotUser != "user-A" {
		t.Errorf("fetcher got user %q want user-A", f.gotUser)
	}
}

// 9.3-UNIT-033: unknown format → error (don't guess).
func TestDump_UnknownFormat_Error(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	_, err := dumps.NewRequestLogsExportDumper(&fakeFetcher{}).Dump(context.Background(), "u-1", "xml", time.Time{}, time.Time{}, &sb)
	if err == nil {
		t.Fatal("expected error for unknown format")
	}
}

// 9.3-BLIND-BOUNDARY-005: zero rows → valid header-only CSV / empty JSONL.
func TestDump_ZeroRows(t *testing.T) {
	t.Parallel()
	csv := dumpCSV(t, nil)
	if lines := csvDataLines(csv); len(lines) != 0 {
		t.Errorf("expected header-only CSV, got data lines: %v", lines)
	}
	if !strings.HasPrefix(csv, "he_request_id,") {
		t.Errorf("header missing on empty CSV")
	}
	var sb strings.Builder
	n, err := dumps.NewRequestLogsExportDumper(&fakeFetcher{rows: nil}).Dump(context.Background(), "u-1", "json", time.Time{}, time.Time{}, &sb)
	if err != nil || n != 0 || sb.Len() != 0 {
		t.Errorf("empty JSONL: n=%d len=%d err=%v", n, sb.Len(), err)
	}
}

// --- helpers ---

func dumpCSV(t *testing.T, rows []dumps.LogRow) string {
	t.Helper()
	var sb strings.Builder
	if _, err := dumps.NewRequestLogsExportDumper(&fakeFetcher{rows: rows}).
		Dump(context.Background(), "u-1", "csv", time.Time{}, time.Time{}, &sb); err != nil {
		t.Fatalf("dump csv: %v", err)
	}
	return sb.String()
}

func firstLine(s string) string { return strings.SplitN(s, "\r\n", 2)[0] }

func csvDataLines(s string) []string {
	parts := strings.Split(strings.TrimSuffix(s, "\r\n"), "\r\n")
	if len(parts) <= 1 {
		return nil
	}
	return parts[1:]
}

// splitCSVLine is a minimal splitter sufficient for the simple test cells
// (no embedded commas in the cells we index for the formula test).
func splitCSVLine(line string) []string { return strings.Split(line, ",") }
