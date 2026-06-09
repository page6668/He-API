// Story 7.1 (AC3 / T3.1+T3.2) — the read-only billing endpoints:
//
//	GET /v1/balance  → PG-authoritative current_usd (BR-A-2: PG, NOT Redis),
//	                   string-decimal money (BR-A-3 / Q-Spec-4).
//	GET /v1/usage    → current-UTC-month aggregate from usage_ledger (BR-A-5),
//	                   string-decimal money + per-model breakdown.
//
// Story 7.2 (AC1) — both endpoints become CURRENCY-AWARE. A stateless
// `?currency=usd|rmb` selector (USD default, Q-PREF) returns ADDITIVE display
// fields (`current_display`/`total_cost_display`, `fx_rate`, `fx_as_of`) as
// string-decimals. The conversion is DISPLAY-ONLY (Q-SOT): USD stays the single
// accounting SoT — NOTHING here writes balances.* or usage_ledger; the rate is
// read from an in-process snapshot (gateway-side, Q-CONVLOC), NEVER the FX
// provider on the hot path (BR-C-5). The USD-default response is byte-compatible
// with the 7.1 contract plus the additive fields (BR-B-7).
//
// Both are mounted behind bearer-API-key auth (user_id from the validated key),
// mirroring the chat hot path. Money is ALWAYS a JSON string (never a number) —
// a JSON-number money field is a contract violation (BR-A-3 / BR-B-4).
package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/he-api/he-api/apps/api-gateway/internal/fxrate"
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

// FxRateSource resolves the latest display-conversion rate from an in-process
// snapshot (Story 7.2, Q-CONVLOC gateway-side). It reads PG-derived state ONLY —
// it NEVER calls the FX provider on the request hot path (BR-C-5). Satisfied by
// *fxrate.Provider; a nil source degrades RMB requests to USD (cold-start guard).
type FxRateSource interface {
	Lookup(base, quote string) (rate decimal.Decimal, fetchedAt time.Time, ok bool)
}

// BillingReadHandler serves GET /v1/balance + GET /v1/usage.
type BillingReadHandler struct {
	logger    *slog.Logger
	db        BillingReadQuerier
	fx        FxRateSource
	fxMissing metric.Int64Counter
	now       func() time.Time
}

// Option configures a BillingReadHandler.
type Option func(*BillingReadHandler)

// WithFxRateSource wires the Story-7.2 currency-conversion snapshot. Absent, the
// endpoints stay USD-only (RMB requests degrade gracefully).
func WithFxRateSource(fx FxRateSource) Option {
	return func(h *BillingReadHandler) { h.fx = fx }
}

// NewBillingReadHandler builds the read handler. logger may be nil.
func NewBillingReadHandler(logger *slog.Logger, db BillingReadQuerier, opts ...Option) *BillingReadHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &BillingReadHandler{logger: logger, db: db, now: time.Now}
	for _, o := range opts {
		o(h)
	}
	// Registered after the global meter provider is installed in cmd/server/main.go
	// (Story 5.3 ISSUE-006 ordering), so the instrument is not orphaned.
	h.fxMissing, _ = otel.Meter("apps/api-gateway/internal/handlers").Int64Counter(
		"he_gateway_fx_rate_missing_total",
		metric.WithDescription("RMB display conversion served degraded due to a missing fx_rates row (cold-start guard)"),
	)
	return h
}

// balanceResponse is the GET /v1/balance body. The 7.1 fields (current_usd,
// currency, updated_at) are preserved; Story 7.2 ADDS current_display/fx_rate/
// fx_as_of (+ fx_degraded only when the cold-start guard fires — omitempty keeps
// the normal path closest to 7.1 bytes).
type balanceResponse struct {
	CurrentUSD     string `json:"current_usd"`
	Currency       string `json:"currency"`
	CurrentDisplay string `json:"current_display"`
	FxRate         string `json:"fx_rate"`
	FxAsOf         string `json:"fx_as_of"`
	FxDegraded     bool   `json:"fx_degraded,omitempty"`
	UpdatedAt      string `json:"updated_at"`
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

	// Parse + validate the display-currency selector BEFORE any PG read — an
	// unsupported currency fails loud, never a silent USD fallback (BR-B-5, INT-013).
	currency, supported := supportedCurrency(r.URL.Query().Get("currency"))
	if !supported {
		raw := strings.TrimSpace(r.URL.Query().Get("currency"))
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unsupported_currency",
			"Unsupported display currency. Supported: USD, RMB.", &raw)
		return
	}

	var (
		raw       string
		updatedAt time.Time
	)
	err := h.db.QueryRow(ctx, balanceSQL, userID).Scan(&raw, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lazily-absent row reads as zero (Q-LAZY). Conversion of 0 is 0.
		fx := h.resolveFx(ctx, currency)
		writeChatJSON(w, http.StatusOK, h.balanceBody(decimal.Zero, fx, h.now().UTC()))
		return
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "billing_balance_read_failed",
			slog.String("event", "billing_balance_read_failed"), slog.String("error", err.Error()))
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read balance.", nil)
		return
	}
	fx := h.resolveFx(ctx, currency)
	writeChatJSON(w, http.StatusOK, h.balanceBody(moneyDecimal(raw), fx, updatedAt.UTC()))
}

// balanceBody renders a balance response from the authoritative USD amount and a
// resolved conversion context. current_usd is ALWAYS present (BR-B-3); the
// converted current_display is ADDITIVE (BR-B-4 string-decimals).
func (h *BillingReadHandler) balanceBody(usd decimal.Decimal, fx fxDisplay, updatedAt time.Time) balanceResponse {
	return balanceResponse{
		CurrentUSD:     usd.StringFixed(moneyScale),
		Currency:       fx.currency,
		CurrentDisplay: fxrate.Convert(usd, fx.rate, fx.scale).StringFixed(fx.scale),
		FxRate:         fx.rate.StringFixed(fxrate.RateScale),
		FxAsOf:         fx.asOf.Format(time.RFC3339),
		FxDegraded:     fx.degraded,
		UpdatedAt:      updatedAt.Format(time.RFC3339),
	}
}

type usageByModel struct {
	Model    string `json:"model"`
	CostUSD  string `json:"cost_usd"`
	Requests int    `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// usageResponse is the GET /v1/usage body. Story 7.2 ADDS currency/
// total_cost_display/fx_rate/fx_as_of on the TOP LINE only; per-model
// by_model[].cost_usd STAYS in USD on ?currency=rmb (documented MVP cut, M-2 /
// Q-APISHAPE) — converting per-model display is deferred to a Console story.
type usageResponse struct {
	Period           string         `json:"period"`
	TotalCostUSD     string         `json:"total_cost_usd"`
	Currency         string         `json:"currency"`
	TotalCostDisplay string         `json:"total_cost_display"`
	FxRate           string         `json:"fx_rate"`
	FxAsOf           string         `json:"fx_as_of"`
	FxDegraded       bool           `json:"fx_degraded,omitempty"`
	TotalRequests    int            `json:"total_requests"`
	TotalTokens      int64          `json:"total_tokens"`
	ByModel          []usageByModel `json:"by_model"`
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

	// Reject an unsupported currency BEFORE any PG read (BR-B-5, INT-016).
	currency, supported := supportedCurrency(r.URL.Query().Get("currency"))
	if !supported {
		raw := strings.TrimSpace(r.URL.Query().Get("currency"))
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_unsupported_currency",
			"Unsupported display currency. Supported: USD, RMB.", &raw)
		return
	}

	resp := usageResponse{
		Period:  h.now().UTC().Format("2006-01"),
		ByModel: []usageByModel{},
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
	totalUSD := moneyDecimal(totalRaw)
	resp.TotalCostUSD = totalUSD.StringFixed(moneyScale)

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
		m.CostUSD = moneyString(costRaw) // by_model stays USD (M-2 MVP cut)
		resp.ByModel = append(resp.ByModel, m)
	}
	if err := rows.Err(); err != nil {
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError,
			"500_internal_error", "Unable to read usage breakdown.", nil)
		return
	}

	// Top-line currency-aware display fields (BR-B-4 string-decimals).
	fx := h.resolveFx(ctx, currency)
	resp.Currency = fx.currency
	resp.TotalCostDisplay = fxrate.Convert(totalUSD, fx.rate, fx.scale).StringFixed(fx.scale)
	resp.FxRate = fx.rate.StringFixed(fxrate.RateScale)
	resp.FxAsOf = fx.asOf.Format(time.RFC3339)
	resp.FxDegraded = fx.degraded

	writeChatJSON(w, http.StatusOK, resp)
}

// fxDisplay is the resolved conversion context for one response.
type fxDisplay struct {
	currency string          // "USD" | "RMB"
	rate     decimal.Decimal // units of display currency per 1 USD
	scale    int32           // display rounding scale (USD echoes 4dp; RMB 2dp)
	asOf     time.Time       // the rate's fetched_at (or now() on the identity/degraded path)
	degraded bool            // cold-start guard fired (no fx_rates row)
}

// oneRate is the USD-identity rate (1.00000000).
var oneRate = decimal.NewFromInt(1)

// resolveFx resolves the conversion context for the requested display currency.
//
//   - USD (default) is the identity: rate 1.0 echoed at 4dp so current_display
//     equals the SoT byte-for-byte (BR-B-7). fx_as_of reflects the latest FX
//     snapshot's fetched_at when available (so a client sees the FX data age).
//   - RMB reads the latest USD→CNY snapshot row (Q-CONVLOC gateway-side).
//   - RMB with NO rate row (cold-start, impossible post-bootstrap-seed) DEGRADES
//     to the USD identity with fx_degraded=true + slog `fx_rate_missing` +
//     metric — NEVER a zero/null rate served (BR / Q-FXFAIL cold-start guard).
func (h *BillingReadHandler) resolveFx(ctx context.Context, currency string) fxDisplay {
	if currency == "USD" {
		asOf := h.now().UTC()
		if h.fx != nil {
			if _, fetchedAt, ok := h.fx.Lookup("USD", "CNY"); ok {
				asOf = fetchedAt.UTC()
			}
		}
		return fxDisplay{currency: "USD", rate: oneRate, scale: fxrate.USDScale, asOf: asOf}
	}

	// RMB display path.
	if h.fx != nil {
		if rate, fetchedAt, ok := h.fx.Lookup("USD", "CNY"); ok {
			return fxDisplay{currency: "RMB", rate: rate, scale: fxrate.DisplayScale, asOf: fetchedAt.UTC()}
		}
	}

	// Cold-start guard — degrade to USD-only, never a zero rate.
	h.logger.WarnContext(ctx, "fx_rate_missing",
		slog.String("event", "fx_rate_missing"),
		slog.String("requested_currency", currency))
	if h.fxMissing != nil {
		h.fxMissing.Add(ctx, 1)
	}
	return fxDisplay{currency: "USD", rate: oneRate, scale: fxrate.USDScale, asOf: h.now().UTC(), degraded: true}
}

// supportedCurrency normalizes the ?currency= selector. Absent ⇒ "usd"
// (BR-B-2); case-insensitive + whitespace-trimmed. Returns the canonical display
// code ("USD"|"RMB") and whether it is supported. MVP = {usd, rmb} exactly —
// any other value is rejected (BR-B-5; no silent USD fallback).
func supportedCurrency(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "usd":
		return "USD", true
	case "rmb":
		return "RMB", true
	default:
		return "", false
	}
}

// moneyDecimal parses a raw NUMERIC text into an exact Decimal. A parse failure
// falls back to zero (defensive — a NUMERIC column always parses).
func moneyDecimal(raw string) decimal.Decimal {
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return d
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
