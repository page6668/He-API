// Package fxrate is the api-gateway's display-currency conversion seam (Story
// 7.2). It is read-side ONLY: it converts the USD accounting truth into a
// presentation amount at the latest USD→CNY rate. USD stays the single
// accounting SoT (Q-SOT) — nothing here writes balances.* or usage_ledger.
//
// The arithmetic is shopspring/decimal end-to-end (HALF-UP, NEVER float64 —
// Architect 7.1 M-1 cascade / Q-ROUND). The rate snapshot (snapshot.go)
// STRUCTURALLY reuses the billing-svc/internal/pricing boot+refresh shape
// (atomic snapshot pointer, ~60s refresh, last-good on error), reading the
// latest fx_rates row per (base,quote) via ORDER BY fetched_at DESC LIMIT 1
// (Architect L-1).
package fxrate

import "github.com/shopspring/decimal"

const (
	// DisplayScale is the 2dp scale for a converted display amount, e.g. RMB
	// (BR-B-6: shopspring/decimal, HALF-UP, display rounded to 2 dp).
	DisplayScale int32 = 2
	// USDScale is the 4dp NUMERIC(12,4) money scale. The USD-default display
	// echoes the SoT at this scale — NOT 2dp-rounded (BR-B-7 / UNIT-007).
	USDScale int32 = 4
	// RateScale is the NUMERIC(18,8) rate scale exposed in the `fx_rate` field.
	RateScale int32 = 8
)

// Convert multiplies a USD amount by an FX rate and rounds HALF-UP (half away
// from zero — shopspring/decimal's Round) to `places` decimal places.
//
// The money path is Decimal end-to-end; a float64 multiply is the M-1 trap this
// deliberately avoids (UNIT-005). The full 8dp rate precision is applied BEFORE
// the result is rounded to `places` (UNIT-006 — precision order). Pure: a
// deterministic function of (usd, rate, places) — no I/O, no clock (UNIT-008).
func Convert(usd, rate decimal.Decimal, places int32) decimal.Decimal {
	return usd.Mul(rate).Round(places)
}
