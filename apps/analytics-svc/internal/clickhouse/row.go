// Package clickhouse is the analytics-svc WRITE side for Story 9.1 AC1 — it maps
// `request.logged` UsageLogEvent payloads to `he_api.request_logs` rows and
// batch-INSERTs them. The actual driver binding (clickhouse-go/v2) lives behind
// the Sink interface (chsink.go) so the row mapping + batching logic are
// unit-testable without a live ClickHouse (integration tests use testcontainers).
package clickhouse

import (
	"errors"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	analyticsv1 "github.com/he-api/he-api/packages/proto/gen/go/he/analytics/v1"
)

// Table is the INSERT target (data-models §4.2).
const Table = "he_api.request_logs"

// insertColumns is the NAMED-COLUMN subset the writer INSERTs (H-3). The event
// is a 21-field subset of the 24-col row; team_id / user_agent / error_message
// are intentionally OMITTED so ClickHouse applies their column defaults (teams
// unrealized; error_message dropped for PII discipline — BR-ING-5).
var insertColumns = []string{
	"he_request_id",
	"user_id",
	"api_key_id",
	"model",
	"upstream_model",
	"routing_strategy",
	"selected_by_strategy",
	"status_code",
	"prompt_tokens",
	"completion_tokens",
	"total_tokens",
	"cost_usd",
	"latency_ms_total",
	"latency_ms_gateway",
	"latency_ms_upstream",
	"ttfb_ms",
	"is_streaming",
	"client_ip",
	"client_country",
	"error_code",
	"ts",
}

// InsertStatement is the named-column INSERT (clickhouse-go/v2 PrepareBatch).
var InsertStatement = "INSERT INTO " + Table + " (" + strings.Join(insertColumns, ", ") + ")"

// heRequestIDRe is the §11.5 taxonomy fence (malformed → DLQ, not a clamp).
var heRequestIDRe = regexp.MustCompile(`^req_[a-f0-9]{12}$`)

// ErrMalformed is returned by EventToRow for a poison event (bad he_request_id
// or missing user_id) — the worker routes these to request.logged.dlq so they
// never block the partition (BR-ING-7).
var ErrMalformed = errors.New("malformed request.logged event")

// Row is the typed request_logs row (Go-native types the driver appends
// directly). Column order matches insertColumns.
type Row struct {
	HeRequestID        string
	UserID             string // UUID string (clickhouse-go accepts string for UUID)
	APIKeyID           string
	Model              string
	UpstreamModel      string
	RoutingStrategy    string
	SelectedByStrategy string
	StatusCode         uint16
	PromptTokens       uint32
	CompletionTokens   uint32
	TotalTokens        uint32
	CostUSD            decimal.Decimal
	LatencyMsTotal     uint32
	LatencyMsGateway   uint32
	LatencyMsUpstream  uint32
	TtfbMs             uint32
	IsStreaming        uint8
	ClientIP           net.IP
	ClientCountry      string // exactly 2 bytes (FixedString(2))
	ErrorCode          string
	Ts                 time.Time
}

// appendArgs returns the row values in insertColumns order for batch.Append.
func (r Row) appendArgs() []any {
	return []any{
		r.HeRequestID,
		r.UserID,
		r.APIKeyID,
		r.Model,
		r.UpstreamModel,
		r.RoutingStrategy,
		r.SelectedByStrategy,
		r.StatusCode,
		r.PromptTokens,
		r.CompletionTokens,
		r.TotalTokens,
		r.CostUSD,
		r.LatencyMsTotal,
		r.LatencyMsGateway,
		r.LatencyMsUpstream,
		r.TtfbMs,
		r.IsStreaming,
		r.ClientIP,
		r.ClientCountry,
		r.ErrorCode,
		r.Ts,
	}
}

// EventToRow validates + maps a UsageLogEvent to a Row. A truly malformed event
// (bad he_request_id / empty user_id) returns ErrMalformed → DLQ. Recoverable
// oddities (out-of-range status, negative/garbage cost, unparseable ts/ip) are
// CLAMPED to safe values with a WARN (9.1-UNIT-011) so one bad field never drops
// an otherwise-attributable row.
func EventToRow(ev *analyticsv1.UsageLogEvent, logger *slog.Logger) (Row, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if !heRequestIDRe.MatchString(ev.GetHeRequestId()) {
		return Row{}, ErrMalformed
	}
	if ev.GetUserId() == "" {
		return Row{}, ErrMalformed
	}

	row := Row{
		HeRequestID:        ev.GetHeRequestId(),
		UserID:             ev.GetUserId(),
		APIKeyID:           ev.GetApiKeyId(),
		Model:              ev.GetModel(),
		UpstreamModel:      ev.GetUpstreamModel(),
		RoutingStrategy:    ev.GetRoutingStrategy(),
		SelectedByStrategy: ev.GetSelectedByStrategy(),
		StatusCode:         clampStatus(ev.GetStatusCode(), ev.GetHeRequestId(), logger),
		PromptTokens:       ev.GetPromptTokens(),
		CompletionTokens:   ev.GetCompletionTokens(),
		TotalTokens:        ev.GetTotalTokens(),
		CostUSD:            parseCost(ev.GetCostUsd(), ev.GetHeRequestId(), logger),
		LatencyMsTotal:     ev.GetLatencyMsTotal(),
		LatencyMsGateway:   ev.GetLatencyMsGateway(),
		LatencyMsUpstream:  ev.GetLatencyMsUpstream(),
		TtfbMs:             ev.GetTtfbMs(),
		IsStreaming:        boolToUint8(ev.GetIsStreaming()),
		ClientIP:           parseIP(ev.GetClientIp()),
		ClientCountry:      country2(ev.GetClientCountry()),
		ErrorCode:          ev.GetErrorCode(),
		Ts:                 parseTS(ev.GetTs()),
	}
	return row, nil
}

func clampStatus(s uint32, heReqID string, logger *slog.Logger) uint16 {
	if s < 100 || s > 599 {
		logger.Warn("request_log_status_clamped",
			slog.String("he_request_id", heReqID), slog.Uint64("status_code", uint64(s)))
		return 0
	}
	return uint16(s)
}

func parseCost(s, heReqID string, logger *slog.Logger) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil || d.IsNegative() {
		logger.Warn("request_log_cost_clamped",
			slog.String("he_request_id", heReqID), slog.String("cost_usd", s))
		return decimal.Zero
	}
	return d
}

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func parseIP(s string) net.IP {
	if ip := net.ParseIP(s); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4
		}
	}
	// request_logs.client_ip is IPv4; default to 0.0.0.0 when absent/non-v4.
	return net.IPv4zero.To4()
}

// country2 normalises to exactly 2 bytes (FixedString(2)). Empty / non-2-char →
// "  " (two spaces) so the fixed-width column never errors.
func country2(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch {
	case len(s) >= 2:
		return s[:2]
	case len(s) == 1:
		return s + " "
	default:
		return "  "
	}
}

func parseTS(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}
