package analyticsquery

// Story 9.2 AC1 — 实时调用日志 read surface over the SAME 9.1 request_logs
// MergeTree (no new store/migration/Kafka topic — BR-RD-10). Every read is
// IDOR-fenced on user_id = $JWT.sub (BR-RD-1): user_id is server-resolved from
// the JWT `sub` and NEVER accepted from a query param. The projection is the
// BR-RD-9 non-PII subset ONLY — client_ip / client_country / user_agent /
// error_message are ops-only (BR-ING-5 read-side discipline) and cost_usd is
// non-authoritative (always 0, H-1-R), so none of them are SELECTed.

import (
	"context"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// logsWindowCap is the "最近 1000 条" recent-window ceiling (BR-RD-2): the
// endpoint exposes at most the 1000 most-recent matching rows, so total_count is
// capped at this value and offset+limit may never exceed it.
const logsWindowCap = 1000

// LogEntry is the per-request projection returned by GET /v1/me/usage/logs. It
// carries ONLY the 13 non-PII observability fields enumerated in BR-RD-9; the
// ops/PII columns and the non-authoritative cost_usd are intentionally absent.
type LogEntry struct {
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

// StatusClass is the canonical status-code bucket (BR-RD-6).
type StatusClass string

const (
	StatusClassSuccess     StatusClass = "success"
	StatusClassClientError StatusClass = "client_error"
	StatusClassServerError StatusClass = "server_error"
)

// LogsFilter is the validated, optional, AND-combined filter set (BR-RD-5). A
// zero value on any field means "no filter on that dimension". user_id is NOT a
// member — it is the IDOR fence, passed separately and always applied (BR-RD-1).
type LogsFilter struct {
	Limit       int
	Offset      int
	Model       string
	Status      StatusClass
	IsStreaming *bool
	APIKeyID    string
	Start       *time.Time
	End         *time.Time
}

// LogsStore is the ClickHouse read surface for the log viewer. Logs MUST inject
// `WHERE user_id = ?` unconditionally (BR-RD-1) and project ONLY the BR-RD-9
// columns. It returns the page rows (most-recent-first) plus the window-capped
// total_count (min(matching, 1000), BR-RD-2).
type LogsStore interface {
	Logs(ctx context.Context, userID string, f LogsFilter) (items []LogEntry, totalCount int, err error)
}

// NewLogsStore wraps an existing driver.Conn as a LogsStore (integration tests).
// Production reuses the OpenStore-dialed *chStore via a type assertion in
// main.go, so no second ClickHouse connection is opened (BR-RD-10).
func NewLogsStore(conn driver.Conn) LogsStore { return &chStore{conn: conn} }

// logsSelectColumns is the EXACT BR-RD-9 projection in LogEntry field order. UUID
// columns are toString()-cast so the driver yields the canonical hyphenated
// string (no dependency on the driver's UUID type mapping). The PII/ops columns
// (client_ip, client_country, user_agent, error_message) and the
// non-authoritative cost_usd are deliberately NEVER selected.
const logsSelectColumns = `he_request_id, ts, model, upstream_model, status_code, ` +
	`is_streaming, prompt_tokens, completion_tokens, total_tokens, ` +
	`latency_ms_total, ttfb_ms, toString(api_key_id) AS api_key_id, error_code`

// buildLogsWhere assembles the IDOR-fenced WHERE clause plus its bound args. The
// FIRST predicate is ALWAYS `user_id = ?` (BR-RD-1) — the fence is unconditional,
// present with zero filters and with every filter. The status class maps to a
// status_code range (BR-RD-6); time bounds are half-open [start, end).
func buildLogsWhere(userID string, f LogsFilter) (string, []any) {
	clauses := []string{"user_id = ?"}
	args := []any{userID}

	if f.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, f.Model)
	}
	switch f.Status {
	case StatusClassSuccess:
		clauses = append(clauses, "status_code < 400")
	case StatusClassClientError:
		clauses = append(clauses, "status_code BETWEEN 400 AND 499")
	case StatusClassServerError:
		clauses = append(clauses, "status_code >= 500")
	}
	if f.IsStreaming != nil {
		v := uint8(0)
		if *f.IsStreaming {
			v = 1
		}
		clauses = append(clauses, "is_streaming = ?")
		args = append(args, v)
	}
	if f.APIKeyID != "" {
		// api_key_id is still fenced to the caller's own keys via user_id above —
		// a foreign key id yields zero rows, never another user's logs (BR-RD-5).
		clauses = append(clauses, "api_key_id = ?")
		args = append(args, f.APIKeyID)
	}
	if f.Start != nil {
		clauses = append(clauses, "ts >= ?")
		args = append(args, f.Start.UTC())
	}
	if f.End != nil {
		clauses = append(clauses, "ts < ?")
		args = append(args, f.End.UTC())
	}
	return strings.Join(clauses, " AND "), args
}

// buildLogsSelect produces the paged, most-recent-first row query. ORDER BY
// ts DESC, he_request_id DESC is a stable tie-break so pages are deterministic
// under equal-ts rows (BR-RD-4).
func buildLogsSelect(userID string, f LogsFilter) (string, []any) {
	where, args := buildLogsWhere(userID, f)
	q := "SELECT " + logsSelectColumns + " FROM he_api.request_logs WHERE " + where +
		" ORDER BY ts DESC, he_request_id DESC LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)
	return q, args
}

// buildLogsCount produces the matching-row count query (capped to the recent
// window by the caller). It carries the identical IDOR-fenced WHERE as the
// SELECT so total_count reflects exactly the rows the user could page through.
func buildLogsCount(userID string, f LogsFilter) (string, []any) {
	where, args := buildLogsWhere(userID, f)
	return "SELECT count() FROM he_api.request_logs WHERE " + where, args
}

// Logs runs the count + page queries against ClickHouse. total_count is capped at
// the 1000-row recent window (BR-RD-2). The rows iterator is released on every
// path (RESOURCE-001).
func (s *chStore) Logs(ctx context.Context, userID string, f LogsFilter) ([]LogEntry, int, error) {
	countQ, countArgs := buildLogsCount(userID, f)
	var matching uint64
	if err := s.conn.QueryRow(ctx, countQ, countArgs...).Scan(&matching); err != nil {
		return nil, 0, err
	}
	totalCount := int(matching)
	if totalCount > logsWindowCap {
		totalCount = logsWindowCap
	}

	selectQ, selectArgs := buildLogsSelect(userID, f)
	rows, err := s.conn.Query(ctx, selectQ, selectArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]LogEntry, 0, f.Limit)
	for rows.Next() {
		var (
			e          LogEntry
			status     uint16
			streaming  uint8
			prompt     uint32
			completion uint32
			total      uint32
			latency    uint32
			ttfb       uint32
		)
		if err := rows.Scan(
			&e.HeRequestID, &e.Ts, &e.Model, &e.UpstreamModel, &status,
			&streaming, &prompt, &completion, &total, &latency, &ttfb,
			&e.ApiKeyID, &e.ErrorCode,
		); err != nil {
			return nil, 0, err
		}
		e.StatusCode = int(status)
		e.IsStreaming = streaming == 1
		e.PromptTokens = int64(prompt)
		e.CompletionTokens = int64(completion)
		e.TotalTokens = int64(total)
		e.LatencyMsTotal = int64(latency)
		e.TtfbMs = int64(ttfb)
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, totalCount, nil
}
