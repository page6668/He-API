package analyticsquery

// Story 9.2 AC1 — GET /v1/me/usage/logs handler. JWT-cookie, per-user
// IDOR-fenced (BR-RD-1), paginated over the 1000-row recent window (BR-RD-2),
// with multi-dimensional AND-combined filtering (BR-RD-5). The query surface is
// strict-rejected: a `user_id` param (IDOR attempt) or any unrecognized key is a
// 400_invalid_request (never trusted), mirroring 9.1's /summary discipline.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

const (
	logsDefaultLimit = 50
	logsMaxLimit     = 100
	logsMaxOffset    = logsWindowCap - 1 // 999 — offset ∈ [0,999] (BR-RD-3)
	logsModelMaxLen  = 128
)

// uuidRe is the RFC4122 UUID surface check for the api_key_id filter (BR-RD-5).
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// allowedLogParams is the strict query-surface whitelist (BR-RD-1 / UNIT-014).
// A `user_id` param or any other unrecognized key is rejected before any store
// call — user_id is resolved ONLY from the JWT sub.
var allowedLogParams = map[string]struct{}{
	"limit": {}, "offset": {}, "model": {}, "status": {},
	"is_streaming": {}, "api_key_id": {}, "start": {}, "end": {},
}

// LogsHandler serves GET /v1/me/usage/logs.
type LogsHandler struct {
	store  LogsStore
	logger *slog.Logger
}

// NewLogsHandler builds the handler. A nil logger falls back to slog.Default().
func NewLogsHandler(store LogsStore, logger *slog.Logger) *LogsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogsHandler{store: store, logger: logger}
}

type logsResponse struct {
	Items      []LogEntry `json:"items"`
	TotalCount int        `json:"total_count"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
	HasMore    bool       `json:"has_more"`
}

// HandleLogs resolves user_id from the JWT, validates+parses the filter params,
// queries ClickHouse (IDOR-fenced), and returns the page. Missing JWT → 401;
// invalid param → 400 (with the offending param); ClickHouse error → 503.
func (h *LogsHandler) HandleLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.UserIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusUnauthorized, "401_unauthenticated", "missing access token", nil)
		return
	}

	filter, badParam, msg := parseLogsFilter(r.URL.Query())
	if badParam != "" {
		p := badParam
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", msg, &p)
		return
	}

	items, totalCount, err := h.store.Logs(ctx, userID, filter)
	if err != nil {
		h.logger.WarnContext(ctx, "usage_logs_clickhouse_unavailable", slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_clickhouse_unavailable",
			"Usage data is temporarily unavailable.", nil)
		return
	}
	if items == nil {
		items = []LogEntry{}
	}

	resp := logsResponse{
		Items:      items,
		TotalCount: totalCount,
		Limit:      filter.Limit,
		Offset:     filter.Offset,
		// has_more = the current page does not reach the (capped) total (BR-RD-2).
		HasMore: filter.Offset+len(items) < totalCount,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // logs are mutable per-second
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// parseLogsFilter validates the query params per the AC1 Data-Validation table.
// On the first invalid field it returns (zero, param, message); on success it
// returns (filter, "", ""). Defaults: limit=50, offset=0, no time bound.
func parseLogsFilter(q url.Values) (LogsFilter, string, string) {
	// Strict-reject any param outside the whitelist (a `user_id` param is an
	// IDOR attempt — BR-RD-1). Done first so an unknown param never silently
	// rides alongside otherwise-valid ones.
	for key := range q {
		if _, ok := allowedLogParams[key]; !ok {
			return LogsFilter{}, key, "unrecognized query parameter"
		}
	}

	f := LogsFilter{Limit: logsDefaultLimit, Offset: 0}

	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > logsMaxLimit {
			return LogsFilter{}, "limit", "limit must be an integer in [1,100]"
		}
		f.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > logsMaxOffset {
			return LogsFilter{}, "offset", "offset must be in [0,999] and offset+limit ≤ 1000"
		}
		f.Offset = n
	}
	// Deep-pagination bound: the queryable window is the 1000 most-recent rows
	// (BR-RD-2). offset=950&limit=100 → 1050 → 400.
	if f.Offset+f.Limit > logsWindowCap {
		return LogsFilter{}, "offset", "offset must be in [0,999] and offset+limit ≤ 1000"
	}

	if v := q.Get("model"); v != "" {
		if len(v) > logsModelMaxLen {
			return LogsFilter{}, "model", "model filter invalid"
		}
		f.Model = v
	}
	if v := q.Get("status"); v != "" {
		switch StatusClass(v) {
		case StatusClassSuccess, StatusClassClientError, StatusClassServerError:
			f.Status = StatusClass(v)
		default:
			return LogsFilter{}, "status", "status must be one of success/client_error/server_error"
		}
	}
	if v := q.Get("is_streaming"); v != "" {
		switch v {
		case "true":
			b := true
			f.IsStreaming = &b
		case "false":
			b := false
			f.IsStreaming = &b
		default:
			return LogsFilter{}, "is_streaming", "is_streaming must be true or false"
		}
	}
	if v := q.Get("api_key_id"); v != "" {
		if !uuidRe.MatchString(v) {
			return LogsFilter{}, "api_key_id", "api_key_id must be a UUID"
		}
		f.APIKeyID = v
	}

	var start, end *time.Time
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return LogsFilter{}, "start", "start/end must be RFC3339 and end > start"
		}
		start = &t
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return LogsFilter{}, "end", "start/end must be RFC3339 and end > start"
		}
		end = &t
	}
	// end must be strictly after start when both are present (BR-RD-5).
	if start != nil && end != nil && !end.After(*start) {
		return LogsFilter{}, "start", "start/end must be RFC3339 and end > start"
	}
	f.Start = start
	f.End = end

	return f, "", ""
}
