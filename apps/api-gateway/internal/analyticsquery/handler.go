// Package analyticsquery serves the Story 9.1 AC2 usage-dashboard read endpoint
// GET /v1/me/usage/summary (JWT-cookie, per-user). Every read is IDOR-fenced on
// user_id = $JWT.sub (BR-RD-1, 5.1 BR-2.5 cascade): user_id is server-resolved
// from the JWT `sub` and NEVER accepted from a query param/body.
//
// Per-metric source-of-truth split (H-1-R / R2-2): 请求数 / 成功率 / Token come
// from ClickHouse (today reads request_logs directly — Q-RT, the MV lags;
// month/quarter read the hourly_agg MV); 消费 (cost) comes from PG usage_ledger
// (the billed-money SoT, summed over the SAME user-tz bounds via the existing
// gateway pgxpool — R2-1/R2-6). No single number is sourced from two stores, so
// the two reads degrade INDEPENDENTLY: a usage_ledger error renders cost_usd=null
// ("—") while the CH cells still render (R2-4); a ClickHouse error returns
// 503_clickhouse_unavailable. Money + success_rate are string-decimals (Q-Spec-4).
// client_ip is NEVER returned (ops/abuse field — BR-ING-5 read-side discipline).
package analyticsquery

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// PeriodAgg is the ClickHouse volume aggregate for one period window (either
// request_logs direct or the hourly_agg MV). Cost is NOT here — 消费 is sourced
// from PG usage_ledger via CostStore (H-1-R / R2-2), not from ClickHouse.
type PeriodAgg struct {
	Requests         int64
	Success          int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Store is the ClickHouse read surface. Both methods MUST parameterize
// user_id = $sub (IDOR fence) — the handler passes the JWT-resolved id only.
type Store interface {
	// Today aggregates request_logs directly (realtime, Q-RT) since the day start.
	Today(ctx context.Context, userID string, since time.Time) (PeriodAgg, error)
	// AggSince aggregates the hourly_agg MV (month/quarter) since the period start.
	AggSince(ctx context.Context, userID string, since time.Time) (PeriodAgg, error)
}

// TimezoneResolver returns the user's *time.Location (users.timezone, Q-TZ).
// Implementations MUST default to UTC on any error (never fail the read).
type TimezoneResolver func(ctx context.Context, userID string) *time.Location

// Handler serves GET /v1/me/usage/summary.
type Handler struct {
	store  Store
	cost   CostStore // 消费 source (PG usage_ledger, H-1-R); nil → cost degrades to null
	tz     TimezoneResolver
	now    func() time.Time
	logger *slog.Logger
}

// NewHandler builds the handler. cost may be nil (→ cost_usd renders null, R2-4);
// tz may be nil (→ UTC); now may be nil (→ time.Now).
func NewHandler(store Store, cost CostStore, tz TimezoneResolver, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if tz == nil {
		tz = func(context.Context, string) *time.Location { return time.UTC }
	}
	return &Handler{store: store, cost: cost, tz: tz, now: time.Now, logger: logger}
}

// WithClock overrides the time source (tests).
func (h *Handler) WithClock(now func() time.Time) *Handler {
	if now != nil {
		h.now = now
	}
	return h
}

type tokenBreakdown struct {
	Prompt     int64 `json:"prompt"`
	Completion int64 `json:"completion"`
	Total      int64 `json:"total"`
}

type period struct {
	Requests    int64          `json:"requests"`
	SuccessRate *string        `json:"success_rate"` // [0,1] string-decimal, or null when requests=0
	Tokens      tokenBreakdown `json:"tokens"`
	CostUsd     *string        `json:"cost_usd"` // usage_ledger SUM string-decimal (H-1-R), or null on usage_ledger read error (R2-4)
}

type summaryResponse struct {
	Today   period `json:"today"`
	Month   period `json:"month"`
	Quarter period `json:"quarter"`
}

// HandleSummary resolves user_id from the JWT, computes the today/month/quarter
// boundaries in the user's timezone, queries ClickHouse, and returns the 3×4
// card matrix. ClickHouse-unavailable → 503_clickhouse_unavailable; any extra
// query param → 400_invalid_request (strict-reject, BR-RD-7).
func (h *Handler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.UserIDFromContext(ctx)
	if !ok {
		_ = openaierr.Write(w, ctx, http.StatusUnauthorized, "401_unauthenticated", "missing access token", nil)
		return
	}
	// BR-RD-7 — summary takes NO query params; a query-param user_id (IDOR
	// attempt) or any extra param is strict-rejected (never trusted).
	if len(r.URL.Query()) > 0 {
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", "This endpoint takes no query parameters.", nil)
		return
	}

	loc := h.tz(ctx, userID)
	todayStart, monthStart, quarterStart := periodStarts(h.now(), loc)
	// The SAME user-tz bounds (in UTC) drive BOTH stores (R2-1).
	todayUTC, monthUTC, quarterUTC := todayStart.UTC(), monthStart.UTC(), quarterStart.UTC()

	// Volume metrics ← ClickHouse. A CH error is fatal to the read (503) — the
	// volume cells have no alternate source (R2-4: cost degrades independently,
	// CH does not).
	todayAgg, err := h.store.Today(ctx, userID, todayUTC)
	if err != nil {
		h.clickhouseDown(w, ctx, "today", err)
		return
	}
	monthAgg, err := h.store.AggSince(ctx, userID, monthUTC)
	if err != nil {
		h.clickhouseDown(w, ctx, "month", err)
		return
	}
	quarterAgg, err := h.store.AggSince(ctx, userID, quarterUTC)
	if err != nil {
		h.clickhouseDown(w, ctx, "quarter", err)
		return
	}

	// 消费 ← PG usage_ledger, IDOR-fenced, same bounds. A usage_ledger error sets
	// ONLY that period's cost_usd to null and does NOT fail the summary (R2-4).
	resp := summaryResponse{
		Today:   toPeriod(todayAgg, h.costFor(ctx, userID, todayUTC)),
		Month:   toPeriod(monthAgg, h.costFor(ctx, userID, monthUTC)),
		Quarter: toPeriod(quarterAgg, h.costFor(ctx, userID, quarterUTC)),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store") // usage is mutable per-second (BR-RD-8)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *Handler) clickhouseDown(w http.ResponseWriter, ctx context.Context, window string, err error) {
	h.logger.WarnContext(ctx, "usage_summary_clickhouse_unavailable",
		slog.String("window", window), slog.String("error", err.Error()))
	_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_clickhouse_unavailable",
		"Usage data is temporarily unavailable.", nil)
}

// toPeriod renders a PeriodAgg + the resolved 消费 into the wire period.
// success_rate is null when requests=0 (the UI renders "—", never NaN —
// BR-RD-4 / BOUNDARY-001). cost is a string-decimal from usage_ledger, or null
// when usage_ledger was unavailable (R2-4 independent degradation).
func toPeriod(a PeriodAgg, cost *string) period {
	var rate *string
	if a.Requests > 0 {
		s := strconv.FormatFloat(float64(a.Success)/float64(a.Requests), 'f', 4, 64)
		rate = &s
	}
	return period{
		Requests:    a.Requests,
		SuccessRate: rate,
		Tokens:      tokenBreakdown{Prompt: a.PromptTokens, Completion: a.CompletionTokens, Total: a.TotalTokens},
		CostUsd:     cost,
	}
}

// costFor reads the period's 消费 from usage_ledger (H-1-R). Returns nil (→ JSON
// null) when there is no cost store wired OR the usage_ledger read errors — the
// 消费 cell degrades independently of the ClickHouse volume cells (R2-4), and a
// usage_ledger fault NEVER emits 503 (that envelope is ClickHouse-specific).
func (h *Handler) costFor(ctx context.Context, userID string, since time.Time) *string {
	if h.cost == nil {
		return nil
	}
	c, err := h.cost.CostSince(ctx, userID, since)
	if err != nil {
		h.logger.WarnContext(ctx, "usage_summary_cost_unavailable",
			slog.String("error", err.Error()))
		return nil
	}
	s := c.StringFixed(costMoneyScale)
	return &s
}

// periodStarts returns the start instants of today / this month / this quarter,
// computed in the user's timezone (Q-TZ). The caller converts to UTC for the
// ClickHouse query (ts is stored UTC).
func periodStarts(now time.Time, loc *time.Location) (today, month, quarter time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	n := now.In(loc)
	today = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	month = time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
	qStartMonth := time.Month((int(n.Month())-1)/3*3 + 1)
	quarter = time.Date(n.Year(), qStartMonth, 1, 0, 0, 0, 0, loc)
	return today, month, quarter
}
