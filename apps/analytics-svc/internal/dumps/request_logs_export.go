// request_logs_export.go — Story 9.3 AC2 format-aware request_logs dumper.
//
// This is the security-critical projection. It exports ONLY the 13 non-PII
// BR-RD-9 columns — EXACTLY the set Story 9.2's
// apps/api-gateway/internal/analyticsquery/logs_store.go `logsSelectColumns`
// projects (Architect Q-COLS ruling; the two are pinned identical by
// TestExportColumns_MatchBRRD9 below — a drift guard, since they live in
// different Go modules so a literal shared import is not possible).
//
// EXCLUDED, NON-NEGOTIABLE (BR-EX-12):
//   - client_ip / client_country / user_agent / error_message  → PII
//   - cost_usd                                                  → non-authoritative
//     "0" (H-1-R; per-request 消费 is NOT exported — usage_ledger is the SoT)
//
// The LogRow struct deliberately has NO field for those columns so a PII/cost
// leak is a compile error, not a code-review miss (statically grep-auditable).
//
// Serialization (BR-EX-11): JSON-Lines (format=json, one object per line) or
// RFC-4180 CSV (format=csv) with a header row, deterministic column order,
// CRLF terminators, RFC-4180 quoting, AND OWASP formula-injection escaping on
// every string cell (model / upstream_model / error_code are upstream-controlled
// → untrusted; Architect Q-CSV mandatory P0).
package dumps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ExportColumns is the canonical, ordered 13-column non-PII projection. It is
// the deterministic CSV column order AND the JSON-Lines key order. Pinned to
// 9.2's logsSelectColumns by TestExportColumns_MatchBRRD9.
var ExportColumns = []string{
	"he_request_id", "ts", "model", "upstream_model", "status_code",
	"is_streaming", "prompt_tokens", "completion_tokens", "total_tokens",
	"latency_ms_total", "ttfb_ms", "api_key_id", "error_code",
}

// LogRowSelectColumns is the ClickHouse SELECT projection — byte-identical to
// 9.2's logsSelectColumns (incl. `toString(api_key_id) AS api_key_id`). The CH
// fetcher uses this so the export and the on-screen viewer cannot drift.
const LogRowSelectColumns = `he_request_id, ts, model, upstream_model, status_code, ` +
	`is_streaming, prompt_tokens, completion_tokens, total_tokens, ` +
	`latency_ms_total, ttfb_ms, toString(api_key_id) AS api_key_id, error_code`

// LogRow is a single exported request_logs row — EXACTLY the 13 BR-RD-9 columns.
// There is intentionally NO client_ip / client_country / user_agent /
// error_message / cost_usd field (BR-EX-12).
type LogRow struct {
	HeRequestID      string    `json:"he_request_id"`
	Ts               time.Time `json:"ts"`
	Model            string    `json:"model"`
	UpstreamModel    string    `json:"upstream_model"`
	StatusCode       int       `json:"status_code"`
	IsStreaming      bool      `json:"is_streaming"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	LatencyMsTotal   int64     `json:"latency_ms_total"`
	TtfbMs           int64     `json:"ttfb_ms"`
	ApiKeyID         string    `json:"api_key_id"`
	ErrorCode        string    `json:"error_code"`
}

// LogRowFetcher reads a user's request_logs rows over [start, end]. The
// implementation MUST fence every query `WHERE user_id = ?` (BR-EX-1 IDOR).
type LogRowFetcher interface {
	FetchLogRows(ctx context.Context, userID string, start, end time.Time) ([]LogRow, error)
}

// RequestLogsExportDumper fetches + serializes a user's request_logs export.
type RequestLogsExportDumper struct {
	Fetcher LogRowFetcher
}

// NewRequestLogsExportDumper constructs the dumper.
func NewRequestLogsExportDumper(fetcher LogRowFetcher) *RequestLogsExportDumper {
	return &RequestLogsExportDumper{Fetcher: fetcher}
}

// Dump writes the user's rows over [start,end] to w in `format` (json|csv) and
// returns the row count. An empty result still writes a valid file (CSV header
// only / no JSONL lines) and returns 0 (BR-EX BLIND-BOUNDARY-005).
func (d *RequestLogsExportDumper) Dump(ctx context.Context, userID, format string, start, end time.Time, w io.Writer) (int64, error) {
	rows, err := d.Fetcher.FetchLogRows(ctx, userID, start, end)
	if err != nil {
		return 0, fmt.Errorf("request_logs dump: fetch: %w", err)
	}
	switch format {
	case "json":
		return writeJSONL(w, rows)
	case "csv":
		return writeCSV(w, rows)
	default:
		// Do NOT guess (BR-EX data-validation): unknown format is a hard error.
		return 0, fmt.Errorf("request_logs dump: unknown format %q", format)
	}
}

// writeJSONL emits one compact JSON object per line (JSON-Lines, BR-EX-11).
func writeJSONL(w io.Writer, rows []LogRow) (int64, error) {
	enc := json.NewEncoder(w) // Encoder writes a trailing '\n' per Encode → JSON-Lines
	for i := range rows {
		if err := enc.Encode(rows[i]); err != nil {
			return int64(i), fmt.Errorf("jsonl encode row %d: %w", i, err)
		}
	}
	return int64(len(rows)), nil
}

// writeCSV emits an RFC-4180 CSV with a header row, deterministic column order,
// CRLF terminators, and OWASP formula-escaped string cells (BR-EX-11 / Q-CSV).
func writeCSV(w io.Writer, rows []LogRow) (int64, error) {
	var b strings.Builder
	// Header row (column names are static + safe, but quoted uniformly).
	writeCSVRecord(&b, ExportColumns)
	for i := range rows {
		r := rows[i]
		writeCSVRecord(&b, []string{
			csvSanitizeCell(r.HeRequestID),
			r.Ts.UTC().Format(time.RFC3339),
			csvSanitizeCell(r.Model),
			csvSanitizeCell(r.UpstreamModel),
			strconv.Itoa(r.StatusCode),
			strconv.FormatBool(r.IsStreaming),
			strconv.FormatInt(r.PromptTokens, 10),
			strconv.FormatInt(r.CompletionTokens, 10),
			strconv.FormatInt(r.TotalTokens, 10),
			strconv.FormatInt(r.LatencyMsTotal, 10),
			strconv.FormatInt(r.TtfbMs, 10),
			csvSanitizeCell(r.ApiKeyID),
			csvSanitizeCell(r.ErrorCode),
		})
	}
	n, err := io.WriteString(w, b.String())
	_ = n
	if err != nil {
		return 0, fmt.Errorf("csv write: %w", err)
	}
	return int64(len(rows)), nil
}

// writeCSVRecord writes one RFC-4180 record terminated by CRLF. Each field is
// quoted+escaped per rfc4180Field.
func writeCSVRecord(b *strings.Builder, fields []string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(rfc4180Field(f))
	}
	b.WriteString("\r\n")
}

// rfc4180Field wraps a field in double quotes (doubling internal quotes) when
// it contains a comma, double-quote, CR, or LF.
func rfc4180Field(s string) string {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// csvSanitizeCell applies OWASP formula-injection escaping: a leading
// formula-trigger char ( = + - @ TAB CR ) is neutralized with a `'` prefix so
// the cell is never evaluated when opened in Excel / Sheets. Applied to
// upstream-controlled string cells (model / upstream_model / error_code are
// untrusted). RFC-4180 quoting is applied AFTER, by rfc4180Field.
func csvSanitizeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	default:
		return s
	}
}
