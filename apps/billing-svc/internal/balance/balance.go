// Package balance is billing-svc's PG-authoritative balance read (Story 7.1
// AC3, BR-A-2). A user checking their balance — or a reconciliation / console
// caller via BillingService.CheckBalance — must see the durable PG truth, NOT
// the possibly-stale Redis realtime mirror (which exists only for the hot-path
// gate where speed > strict accuracy).
package balance

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Scale is the money scale: NUMERIC(12,4).
const Scale int32 = 4

// Zero is the string-decimal money form of an absent balance (a lazily-uncreated
// balances row reads as zero — AC3 error-handling row).
const Zero = "0.0000"

const readSQL = `SELECT current_usd::text FROM he_api.balances WHERE user_id = $1`

// Querier is the minimal pgx surface Read needs (satisfied by *pgxpool.Pool and
// pgxmock).
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Result is a PG-authoritative balance read.
type Result struct {
	CurrentUSD string // string-decimal money, always 4dp ("12.3400")
	Sufficient bool   // current_usd > 0 (the hard-zero 7.1 gate threshold, BR-A-6)
}

// Read returns the authoritative balance for userID. An absent row reads as
// "0.0000" / not-sufficient (Q-LAZY lazy-create — the row appears on first
// deduction, not at signup).
func Read(ctx context.Context, q Querier, userID string) (Result, error) {
	var raw string
	err := q.QueryRow(ctx, readSQL, userID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{CurrentUSD: Zero, Sufficient: false}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return normalize(raw), nil
}

// normalize formats a raw NUMERIC text into the canonical 4dp money string and
// derives sufficiency (> 0). A parse failure (should never happen for a NUMERIC
// column) falls back to the raw value, not-sufficient.
func normalize(raw string) Result {
	d, err := decimal.NewFromString(raw)
	if err != nil {
		return Result{CurrentUSD: raw, Sufficient: false}
	}
	return Result{
		CurrentUSD: d.StringFixed(Scale),
		Sufficient: d.IsPositive(),
	}
}
