// Package pricing is billing-svc's cost engine (Story 7.1 AC1). It is the
// financial-accuracy core: a pure ComputeCost over an immutable model_pricing
// Snapshot, using shopspring/decimal end-to-end (Architect Q-DECIMAL + M-1 —
// NEVER float64 on the money path) and HALF-UP rounding to NUMERIC(12,4)
// (Q-ROUND).
//
// The snapshot loader (loader.go) STRUCTURALLY reuses the Story-6.2
// routing-svc/internal/pricing shape (boot snapshot + 60s refresh, last-good on
// error), but its arithmetic is Decimal, not the routing float64 (which only
// RANKS by price and never charges — copying that float math onto the money
// path is the M-1 trap this package deliberately avoids).
package pricing

import (
	"errors"

	"github.com/shopspring/decimal"

	billingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/billing/v1"
)

// CostScale is the money scale: NUMERIC(12,4) → 4 fractional digits (Q-ROUND).
const CostScale int32 = 4

// markup bounds — a row outside [5,15] is a pricing-data error: billing WARNs
// (caller emits slog `billing_markup_out_of_range`) but bills as-is, never
// silently dropping a charge (AC1 / Q-ROUND out-of-range).
var (
	markupMin = decimal.NewFromInt(5)
	markupMax = decimal.NewFromInt(15)

	thousand = decimal.NewFromInt(1000)
	hundred  = decimal.NewFromInt(100)
	sixty    = decimal.NewFromInt(60) // Story 9.6 — seconds-per-minute (ceil-to-second ASR billing)
	one      = decimal.NewFromInt(1)
)

// ErrNoPricing is returned by ComputeCost when the model has no pricing row in
// the snapshot. The caller (internal/ledger) MUST dead-letter the event — never
// silently zero-charge a billable request (AC1 / BR-D-8).
var ErrNoPricing = errors.New("billing/pricing: no pricing row for model")

// Row is one model's pricing, parsed exactly from the NUMERIC columns (read as
// text, never float). PerCallPrice is nil unless the model_pricing.per_call_-
// price_usd column is set (Q-PERCALL — UNWIRED/absent for every model in 7.1).
type Row struct {
	PriceIn      decimal.Decimal // USD per 1k input tokens
	PriceOut     decimal.Decimal // USD per 1k output tokens
	Markup       decimal.Decimal // percent, e.g. 10.00
	PerCallPrice *decimal.Decimal
	// PricePerMinuteAudio (Story 9.6) — USD per audio MINUTE for PER_MINUTE
	// (ASR) billing. nil for every token model (the nullable
	// model_pricing.price_per_minute_audio_usd column); set for doubao-asr.
	PricePerMinuteAudio *decimal.Decimal
}

// Snapshot is an immutable model_id → Row view. The zero value (and a nil
// *Snapshot) is a valid empty snapshot — ComputeCost reports ErrNoPricing for
// every model rather than panicking (boot-before-PG resilience).
type Snapshot struct {
	rows map[string]Row
}

// NewSnapshot builds an immutable Snapshot from a model_id → Row map. The input
// is defensively copied.
func NewSnapshot(rows map[string]Row) *Snapshot {
	cp := make(map[string]Row, len(rows))
	for k, v := range rows {
		cp[k] = v
	}
	return &Snapshot{rows: cp}
}

// Len reports the number of priced models.
func (s *Snapshot) Len() int {
	if s == nil {
		return 0
	}
	return len(s.rows)
}

// Row returns the pricing row for modelID and whether one exists.
func (s *Snapshot) Row(modelID string) (Row, bool) {
	if s == nil || s.rows == nil {
		return Row{}, false
	}
	r, ok := s.rows[modelID]
	return r, ok
}

// Result is the output of ComputeCost. MarkupOutOfRange is true when the row's
// markup is ∉ [5,15] — the caller emits the slog WARN (kept OUT of ComputeCost
// so the function stays pure: no I/O, no clock — BR-C-5, the golden-vector seam).
type Result struct {
	Cost             decimal.Decimal
	MarkupOutOfRange bool
}

// ComputeCost is the pure cost function (BR-C-5): a deterministic function of
// (modelID, tokens, mode, snapshot) — no I/O, no clock, fully unit-testable with
// golden vectors.
//
// Per-token (the only billed mode in 7.1):
//
//	base     = prompt/1000 × price_in + completion/1000 × price_out
//	cost_usd = base × (1 + markup_percent/100), HALF-UP to NUMERIC(12,4)
//
// Per-call (Q-PERCALL — OFF in 7.1; dormant forward-compat): when mode is
// PER_CALL and the row carries per_call_price_usd, the flat per-call fee
// REPLACES the per-token cost (Architect ruling: replace, not add).
//
// All arithmetic is Decimal — NEVER float64 (M-1). Rounding is half-away-from-
// zero == HALF-UP for the non-negative money domain (Q-ROUND).
func (s *Snapshot) ComputeCost(modelID string, promptTokens, completionTokens uint32, audioDurationSeconds float64, mode billingv1.BillingMode) (Result, error) {
	row, ok := s.Row(modelID)
	if !ok {
		return Result{}, ErrNoPricing
	}

	outOfRange := row.Markup.LessThan(markupMin) || row.Markup.GreaterThan(markupMax)
	multiplier := one.Add(row.Markup.Div(hundred))

	// Story 9.6 — PER_MINUTE (ASR) mode REPLACES per-token: bill per audio
	// duration, ceil-to-SECOND (Q-ASR-BILLING RATIFIED):
	//
	//	cost = ceil(duration_seconds) × price_per_minute_audio_usd/60 × (1+markup)
	//
	// Fail-CLOSED: a PER_MINUTE event for a model with NO per-minute price →
	// ErrNoPricing (the caller DLQs; never a silent zero-cost ledger row —
	// BR-D-3). The duration arrives as a proto double; NewFromFloat→Ceil pins it
	// to an exact integer-second count BEFORE any money arithmetic, so the
	// per-second math stays pure-Decimal (M-1 — no float64 on the money path).
	if mode == billingv1.BillingMode_BILLING_MODE_PER_MINUTE {
		if row.PricePerMinuteAudio == nil {
			return Result{}, ErrNoPricing
		}
		seconds := decimal.NewFromFloat(audioDurationSeconds).Ceil()
		cost := seconds.Mul(row.PricePerMinuteAudio.Div(sixty)).Mul(multiplier).Round(CostScale)
		return Result{Cost: cost, MarkupOutOfRange: outOfRange}, nil
	}

	// Per-call mode REPLACES per-token when enabled for the model (Q-PERCALL).
	// Unreachable in 7.1 (no row carries PerCallPrice), but wired for forward-
	// compat (UNIT-017).
	if mode == billingv1.BillingMode_BILLING_MODE_PER_CALL && row.PerCallPrice != nil {
		return Result{
			Cost:             row.PerCallPrice.Round(CostScale),
			MarkupOutOfRange: outOfRange,
		}, nil
	}

	prompt := decimal.NewFromInt(int64(promptTokens))
	completion := decimal.NewFromInt(int64(completionTokens))

	base := prompt.Div(thousand).Mul(row.PriceIn).
		Add(completion.Div(thousand).Mul(row.PriceOut))

	cost := base.Mul(multiplier).Round(CostScale)

	return Result{Cost: cost, MarkupOutOfRange: outOfRange}, nil
}
