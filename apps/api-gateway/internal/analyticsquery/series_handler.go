package analyticsquery

// Story 9.1b AC1 — GET /v1/me/usage/series handler. JWT-cookie, per-user
// IDOR-fenced (BR-CH-SERIES-3), day-bucketed over a bounded range (≤90d), with a
// By Day / By Model / By Status grouping toggle (BR-CH-SERIES-2). The query
// surface is strict-rejected: a `user_id` param (an IDOR attempt) or any
// unrecognized key is a 400_invalid_request (never trusted) — user_id is resolved
// ONLY from the JWT sub, mirroring 9.1 /summary + 9.2 /logs discipline (BR-RD-7).
//
// Cost-SoT (BR-CH-SERIES-2 / H-1-R): under the SM default for Q-SERIES-COST the
// trend carries NO cost series. Each point therefore OMITS cost_usd entirely
// (`omitempty`) rather than emit a non-authoritative request_logs.cost_usd (always
// 0) — the wire contract is `cost_usd: string | absent`. A usage_ledger-backed
// cost series (PG, never ClickHouse) is the only way it could ever be surfaced.

import (
	"context"
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
	seriesDefaultRangeDays = 30 // absent range → 30d (the P-4 default)
	seriesMaxRangeDays     = 90 // BR-CH-SERIES / BR-RD-7 upper bound
)

// rangeRe matches the "<N>d" range token (e.g. "30d"). A bare "30", "abc", or
// "0d" fails the [1,90] bound below — all → 400_invalid_request.
var rangeRe = regexp.MustCompile(`^([0-9]+)d$`)

// allowedSeriesParams is the strict query-surface whitelist (BR-CH-SERIES-3).
// A `user_id` param or any other unrecognized key is rejected before any store
// call — user_id is resolved ONLY from the JWT sub.
var allowedSeriesParams = map[string]struct{}{
	"group_by": {}, "range": {},
}

// SeriesHandler serves GET /v1/me/usage/series.
type SeriesHandler struct {
	store  SeriesStore
	tz     TimezoneResolver
	now    func() time.Time
	logger *slog.Logger
}

// NewSeriesHandler builds the handler. tz may be nil (→ UTC); a nil logger falls
// back to slog.Default().
func NewSeriesHandler(store SeriesStore, tz TimezoneResolver, logger *slog.Logger) *SeriesHandler {
	if logger == nil {
		logger = slog.Default()
	}
	if tz == nil {
		tz = utcResolver
	}
	return &SeriesHandler{store: store, tz: tz, now: time.Now, logger: logger}
}

func utcResolver(context.Context, string) *time.Location { return time.UTC }

// WithClock overrides the time source (tests).
func (h *SeriesHandler) WithClock(now func() time.Time) *SeriesHandler {
	if now != nil {
		h.now = now
	}
	return h
}

// seriesPointWire is one trend bucket on the wire. cost_usd is omitempty and
// always nil under the SM default (no cost series — H-1-R / BR-CH-SERIES-2).
type seriesPointWire struct {
	Bucket      string  `json:"bucket"`
	Key         *string `json:"key"`
	Requests    int64   `json:"requests"`
	TotalTokens int64   `json:"total_tokens"`
	CostUsd     *string `json:"cost_usd,omitempty"`
}

type seriesResponse struct {
	Range   string            `json:"range"`
	GroupBy string            `json:"group_by"`
	Series  []seriesPointWire `json:"series"`
}

// HandleSeries resolves user_id from the JWT, validates+parses group_by/range,
// computes the range start in the user's timezone, queries ClickHouse
// (IDOR-fenced), and returns the bucketed trend. Missing JWT → 401; invalid
// param → 400 (with the offending param); ClickHouse error → 503.
func (h *SeriesHandler) HandleSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.UserIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusUnauthorized, "401_unauthenticated", "missing access token", nil)
		return
	}

	gb, rangeDays, rangeStr, badParam, msg := parseSeriesQuery(r.URL.Query())
	if badParam != "" {
		p := badParam
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", msg, &p)
		return
	}

	// Range start = start-of-today in the user's tz, shifted back (rangeDays-1)
	// whole days, so a 30d window spans today + 29 prior local days (Q-TZ parity
	// with /summary's period boundaries).
	loc := h.tz(ctx, userID)
	if loc == nil {
		loc = time.UTC
	}
	startToday := startOfDay(h.now(), loc)
	since := startToday.AddDate(0, 0, -(rangeDays - 1)).UTC()

	points, err := h.store.Series(ctx, userID, gb, since, loc)
	if err != nil {
		h.logger.WarnContext(ctx, "usage_series_clickhouse_unavailable",
			slog.String("group_by", string(gb)), slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_clickhouse_unavailable",
			"Usage data is temporarily unavailable.", nil)
		return
	}

	resp := seriesResponse{Range: rangeStr, GroupBy: string(gb), Series: toSeriesWire(points)}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // usage is mutable per-second (BR-RD-8)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// toSeriesWire maps store points to the wire shape. series is ALWAYS a non-nil
// array — no history → `[]`, HTTP 200 (BLIND-BOUNDARY-001), never JSON null.
// cost_usd is left nil (→ omitempty → absent) under the SM default.
func toSeriesWire(points []SeriesPoint) []seriesPointWire {
	out := make([]seriesPointWire, 0, len(points))
	for _, p := range points {
		out = append(out, seriesPointWire{
			Bucket:      p.Bucket,
			Key:         p.Key,
			Requests:    p.Requests,
			TotalTokens: p.TotalTokens,
		})
	}
	return out
}

// parseSeriesQuery validates the query per the AC1 Data-Validation table. On the
// first invalid field it returns (_, _, _, param, message); on success it returns
// (gb, rangeDays, normalizedRange, "", ""). Defaults: group_by=day, range=30d.
func parseSeriesQuery(q url.Values) (gb SeriesGroupBy, rangeDays int, rangeStr, badParam, msg string) {
	// Strict-reject any param outside the whitelist (a `user_id` param is an IDOR
	// attempt — BR-CH-SERIES-3). Done first so an unknown param never silently
	// rides alongside otherwise-valid ones.
	for key := range q {
		if _, ok := allowedSeriesParams[key]; !ok {
			return "", 0, "", key, "unrecognized query parameter"
		}
	}

	gb = GroupByDay
	if v := q.Get("group_by"); v != "" {
		switch SeriesGroupBy(v) {
		case GroupByDay, GroupByModel, GroupByStatus:
			gb = SeriesGroupBy(v)
		default:
			return "", 0, "", "group_by", "group_by must be one of day/model/status"
		}
	}

	rangeDays = seriesDefaultRangeDays
	rangeStr = strconv.Itoa(seriesDefaultRangeDays) + "d"
	if v := q.Get("range"); v != "" {
		m := rangeRe.FindStringSubmatch(v)
		if m == nil {
			return "", 0, "", "range", "range must be of the form <N>d with 1 ≤ N ≤ 90"
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n < 1 || n > seriesMaxRangeDays {
			return "", 0, "", "range", "range must be of the form <N>d with 1 ≤ N ≤ 90"
		}
		rangeDays = n
		rangeStr = v
	}

	return gb, rangeDays, rangeStr, "", ""
}

// startOfDay returns midnight of `now` in loc (the local-day start). The caller
// converts to UTC for the ClickHouse query (ts/hour are stored UTC).
func startOfDay(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	n := now.In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
}
