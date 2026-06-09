// Story 7.1 (AC3 / T3.1+T3.2) — the read-only billing endpoints:
//
//	GET /v1/balance  → PG-authoritative current_usd (BR-A-2: PG, NOT Redis),
//	                   string-decimal money (BR-A-3 / Q-Spec-4).
//	GET /v1/usage    → current-UTC-month aggregate from usage_ledger (BR-A-5),
//	                   string-decimal money + per-model breakdown.
//
// Both are mounted behind bearer-API-key auth (user_id from the validated key),
// mirroring the chat hot path. Money is ALWAYS a JSON string (never a number) —
// a JSON-number money field is a contract violation (BR-A-3).
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// moneyScale is the NUMERIC(12,4) money scale.
const moneyScale int32 = 4

// BillingReadQuerier is the minimal pgx surface the read endpoints need
// (satisfied by *pgxpool.Pool and pgxmock).
type BillingReadQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// BillingReadHandler serves GET /v1/balance + GET /v1/usage.
type BillingReadHandler struct {
	logger *slog.Logger
	db     BillingReadQuerier
	now    func() time.Time
}

// NewBillingReadHandler builds the read handler. logger may be nil.
func NewBillingReadHandler(logger *slog.Logger, db BillingReadQuerier) *BillingReadHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &BillingReadHandler{logger: logger, db: db, now: time.Now}
}

type balanceResponse struct {
	CurrentUSD string `json:"current_usd"`
	Currency   string `json:"currency"`
	UpdatedAt  string `json:"updated_at"`
}

const balanceSQL = `SELECT current_usd::text, updated_at FROM he_api.balances WHERE user_id = $1`

// Balance handles GET /v1/balance.
func (h *BillingReadHandler) Balance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}

	var (
		raw       string
		updatedAt time.Time
	)
	err := h.db.QueryRow(ctx, balanceSQL, userID).Scan(&raw, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lazily-absent row reads as zero (Q-LAZY).
		writeChatJSON(w, http.StatusOK, balanceResponse{
			CurrentUSD: "0.0000", Currency: "USD",
			UpdatedAt: h.now().UTC().Format(time.RFC3339),
		})
		return
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "billing_balance_read_failed",
			slog.String("event", "billing_balance_read_failed"), slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read balance.", nil)
		return
	}
	writeChatJSON(w, http.StatusOK, balanceResponse{
		CurrentUSD: moneyString(raw),
		Currency:   "USD",
		UpdatedAt:  updatedAt.UTC().Format(time.RFC3339),
	})
}

type usageByModel struct {
	Model    string `json:"model"`
	CostUSD  string `json:"cost_usd"`
	Requests int    `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

type usageResponse struct {
	Period        string         `json:"period"`
	TotalCostUSD  string         `json:"total_cost_usd"`
	TotalRequests int            `json:"total_requests"`
	TotalTokens   int64          `json:"total_tokens"`
	ByModel       []usageByModel `json:"by_model"`
}

const (
	usageTotalsSQL = `SELECT COALESCE(SUM(cost_usd),0)::text, COUNT(*), COALESCE(SUM(prompt_tokens + completion_tokens),0)
FROM he_api.usage_ledger
WHERE user_id = $1 AND ts >= date_trunc('month', now() AT TIME ZONE 'UTC')`

	usageByModelSQL = `SELECT model, COALESCE(SUM(cost_usd),0)::text, COUNT(*), COALESCE(SUM(prompt_tokens + completion_tokens),0)
FROM he_api.usage_ledger
WHERE user_id = $1 AND ts >= date_trunc('month', now() AT TIME ZONE 'UTC')
GROUP BY model ORDER BY model`
)

// Usage handles GET /v1/usage — the current-UTC-month aggregate (BR-A-5, aligned
// with the Story 5.4 monthly cap reset boundary).
func (h *BillingReadHandler) Usage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := middleware.BearerUserIDFromContext(ctx)
	if !ok || userID == "" {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_gateway_misconfigured", "Bearer-auth middleware not wired", nil)
		return
	}

	resp := usageResponse{
		Period:       h.now().UTC().Format("2006-01"),
		TotalCostUSD: "0.0000",
		ByModel:      []usageByModel{},
	}

	var totalRaw string
	if err := h.db.QueryRow(ctx, usageTotalsSQL, userID).
		Scan(&totalRaw, &resp.TotalRequests, &resp.TotalTokens); err != nil {
		h.logger.ErrorContext(ctx, "billing_usage_read_failed",
			slog.String("event", "billing_usage_read_failed"), slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read usage.", nil)
		return
	}
	resp.TotalCostUSD = moneyString(totalRaw)

	rows, err := h.db.Query(ctx, usageByModelSQL, userID)
	if err != nil {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read usage breakdown.", nil)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var m usageByModel
		var costRaw string
		if err := rows.Scan(&m.Model, &costRaw, &m.Requests, &m.Tokens); err != nil {
			_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
				"500_internal_error", "Unable to read usage breakdown.", nil)
			return
		}
		m.CostUSD = moneyString(costRaw)
		resp.ByModel = append(resp.ByModel, m)
	}
	if err := rows.Err(); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read usage breakdown.", nil)
		return
	}

	writeChatJSON(w, http.StatusOK, resp)
}

// moneyString normalizes a raw NUMERIC text into the canonical 4dp string money
// form ("12.3400"). A parse failure falls back to the raw value (defensive — a
// NUMERIC column always parses).
func moneyString(raw string) string {
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return raw
	}
	return d.StringFixed(moneyScale)
}
