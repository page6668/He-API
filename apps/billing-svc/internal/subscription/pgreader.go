package subscription

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// RowQuerier is the minimal pgx surface the PG reader needs (satisfied by
// *pgxpool.Pool + pgxmock).
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readActiveSubSQL reads the caller's ACTIVE subscription off the 7.3
// he_api.subscriptions rail (0010). Only an active row binds a tier; a
// cancelled/past_due row resolves to free (Q-PLAN-DEFAULT, BR-S-8), so the
// reader filters on status='active'. There is exactly one active subscription
// per user (the singleton sub-resource — Medium-2).
const readActiveSubSQL = `SELECT plan, status, payment_provider, external_subscription_id,
		COALESCE(to_char(current_period_end, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '')
	FROM he_api.subscriptions
	WHERE user_id = $1 AND status = 'active'
	ORDER BY current_period_end DESC NULLS LAST
	LIMIT 1`

// PGSubReader reads the current subscription from PG. Satisfies SubReader.
type PGSubReader struct {
	db RowQuerier
}

// NewPGSubReader builds the reader over a pgx query surface.
func NewPGSubReader(db RowQuerier) *PGSubReader { return &PGSubReader{db: db} }

// CurrentSubscription returns the user's active subscription. found == false
// when there is no active row (the resolver then treats the user as free).
func (r *PGSubReader) CurrentSubscription(ctx context.Context, userID string) (Subscription, bool, error) {
	var plan, status, provider, extID, periodEnd string
	err := r.db.QueryRow(ctx, readActiveSubSQL, userID).Scan(&plan, &status, &provider, &extID, &periodEnd)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Subscription{}, false, nil
		}
		return Subscription{}, false, fmt.Errorf("subscription: read current: %w", err)
	}
	return Subscription{
		Plan:                   plancatalogue.PlanKey(plan),
		Status:                 status,
		Provider:               provider,
		ExternalSubscriptionID: extID,
		CurrentPeriodEnd:       periodEnd,
	}, true, nil
}
