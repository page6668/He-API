// Package recharge is billing-svc's writer for he_api.recharge_orders (Story 7.3,
// Q-ORDEROWNER — billing-svc is the SOLE writer of recharge_orders, keeping the
// order state-machine in the same service as the balance so the credit + the
// pending→paid flip can share one PG tx). It realises the §5.2-sketched
// BillingService.CreateRechargeOrder RPC: payment-svc calls it to persist the
// PENDING order, gets back our internal order id, and carries it into the
// provider checkout as metadata (so the webhook resolves OUR order — BR-R-4).
package recharge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

// Querier is the minimal pgx surface Create needs (satisfied by *pgxpool.Pool +
// pgxmock).
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// insertSQL writes a PENDING order. external_order_id is left NULL — it is bound
// at credit time from the provider-verified webhook (the UNIQUE(payment_provider,
// external_order_id) fence engages then). RETURNING the generated UUID.
const insertSQL = `INSERT INTO he_api.recharge_orders
	(user_id, amount, currency, payment_provider, status, created_at)
VALUES ($1, $2::numeric, $3, $4, 'pending', NOW())
RETURNING id::text`

// ErrInvalid marks a rejected create (bad amount / missing field). The gRPC
// handler maps it to InvalidArgument.
var ErrInvalid = errors.New("recharge: invalid order")

// Writer persists pending recharge orders.
type Writer struct {
	db Querier
}

// New builds a Writer.
func New(db Querier) *Writer { return &Writer{db: db} }

// Create inserts a PENDING recharge order and returns its internal id. amount is
// a string-decimal intent (Q-Spec-4); it is validated > 0 before the write.
func (w *Writer) Create(ctx context.Context, userID, amount, currency, provider string) (string, error) {
	userID = strings.TrimSpace(userID)
	provider = strings.TrimSpace(provider)
	currency = strings.TrimSpace(currency)
	if userID == "" || provider == "" || currency == "" {
		return "", ErrInvalid
	}
	amt, err := decimal.NewFromString(strings.TrimSpace(amount))
	if err != nil || !amt.IsPositive() {
		return "", ErrInvalid
	}
	var id string
	if err := w.db.QueryRow(ctx, insertSQL, userID, amt.StringFixed(4), currency, provider).Scan(&id); err != nil {
		return "", fmt.Errorf("recharge: insert order: %w", err)
	}
	return id, nil
}
