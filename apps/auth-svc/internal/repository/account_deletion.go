package repository

import (
	"context"

	"github.com/google/uuid"
)

// DueUser is the minimal projection the account-deletion-sweeper (Story 2.7
// AC6) needs per row: the id to erase + the ORIGINAL email/locale/display_name
// captured BEFORE anonymization so the completion email can still be sent
// (BR-6.4 — read at selection, send after PG commit).
type DueUser struct {
	ID          uuid.UUID
	Email       string
	Locale      string
	DisplayName *string
}

const (
	// selectDueDeletionsSQL drives the daily scan (BR-6.2). Strict `<=`. Uses
	// the partial index idx_users_pending_deletion_due (AC5 BR-5.2) so the scan
	// only touches pending rows.
	selectDueDeletionsSQL = `SELECT id, email, locale, display_name
FROM he_api.users
WHERE status='pending_deletion' AND pending_deletion_at <= NOW()`

	// selectReconcileDeletionsSQL drives the crash-safety reconcile (BR-6.6): a
	// row already PG-anonymized (status='deleted') but whose cross-store scrub
	// (ClickHouse / OSS / Stripe) did NOT complete (anonymized_at IS NULL) is
	// re-swept for those steps ONLY — the PG anonymize is not repeated.
	selectReconcileDeletionsSQL = `SELECT id, email, locale, display_name
FROM he_api.users
WHERE status='deleted' AND anonymized_at IS NULL`

	// anonymizeUserPGSQL is the AC6 step-1 SOFT-delete (BR-6.3). The users row is
	// RETAINED (status='deleted') for legal/financial linkage — NOT physically
	// deleted — which is why the PII children MUST be explicitly deleted (CASCADE
	// only fires on row DELETE, which we never do). The email rewrite preserves
	// the UNIQUE constraint and is irreversible. anonymized_at is deliberately
	// LEFT NULL here (set only after the cross-store scrub — BR-6.6). The
	// `status='pending_deletion'` guard makes this a no-op on the reconcile path.
	anonymizeUserPGSQL = `UPDATE he_api.users
SET email = 'deleted+' || id::text || '@anonymized.invalid',
    display_name = NULL,
    password_hash = NULL,
    oauth_subject = NULL,
    oauth_provider = NULL,
    totp_secret_encrypted = NULL,
    totp_enabled = FALSE,
    status = 'deleted',
    deleted_at = NOW(),
    updated_at = NOW()
WHERE id=$1 AND status='pending_deletion'`

	// markAnonymizationCompleteSQL closes the reconcile gate (BR-6.6): set only
	// after ClickHouse + OSS + Stripe-detach all succeed.
	markAnonymizationCompleteSQL = `UPDATE he_api.users
SET anonymized_at = NOW(), updated_at = NOW()
WHERE id=$1 AND status='deleted' AND anonymized_at IS NULL`

	// Explicit PII-child deletes (BR-6.3 — CASCADE does NOT fire on the UPDATE
	// soft-delete). api_keys / mfa_recovery_codes / data_export_requests are
	// ON DELETE CASCADE on a row DELETE, but we soft-delete, so they MUST be
	// removed explicitly. balances is also explicitly deleted.
	deleteAPIKeysForUserSQL        = `DELETE FROM he_api.api_keys WHERE user_id=$1`
	deleteDataExportReqForUserSQL  = `DELETE FROM he_api.data_export_requests WHERE user_id=$1`
	deleteBalancesForUserSQL       = `DELETE FROM he_api.balances WHERE user_id=$1`
	selectPaymentMethodTokensSQL   = `SELECT provider_pm_token FROM he_api.payment_methods WHERE user_id=$1`
	deletePaymentMethodsForUserSQL = `DELETE FROM he_api.payment_methods WHERE user_id=$1`
)

// SelectDueDeletions returns the users whose grace window has elapsed.
func SelectDueDeletions(ctx context.Context, q Querier) ([]DueUser, error) {
	return scanDueUsers(ctx, q, selectDueDeletionsSQL)
}

// SelectReconcileDeletions returns PG-anonymized rows whose cross-store scrub
// did not complete (BR-6.6).
func SelectReconcileDeletions(ctx context.Context, q Querier) ([]DueUser, error) {
	return scanDueUsers(ctx, q, selectReconcileDeletionsSQL)
}

func scanDueUsers(ctx context.Context, q Querier, sql string) ([]DueUser, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueUser
	for rows.Next() {
		var u DueUser
		if err := rows.Scan(&u.ID, &u.Email, &u.Locale, &u.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AnonymizeUserPG performs the AC6 step-1 soft-delete. Returns (true, nil) when
// a pending row was anonymized; (false, nil) when the guard matched nothing
// (already deleted — reconcile path); (false, err) on driver failure.
func AnonymizeUserPG(ctx context.Context, q Querier, userID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, anonymizeUserPGSQL, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// DeletePIIChildren explicitly removes the PII-bearing child rows that CASCADE
// would have removed on a row DELETE but does NOT on our soft-delete UPDATE
// (BR-6.3 — the single most error-prone point of the story). mfa_recovery_codes
// reuses the Story 2.4 helper. Runs inside the per-user PG transaction.
func DeletePIIChildren(ctx context.Context, q Querier, userID uuid.UUID) error {
	if _, err := q.Exec(ctx, deleteAPIKeysForUserSQL, userID); err != nil {
		return err
	}
	if err := DeleteAllRecoveryCodesForUser(ctx, q, userID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, deleteDataExportReqForUserSQL, userID); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, deleteBalancesForUserSQL, userID); err != nil {
		return err
	}
	return nil
}

// SelectPaymentMethodTokens reads the live Stripe PaymentMethod tokens for a
// user BEFORE the rows are deleted, so the sweeper can detach them upstream
// (OQ-3 OVERRIDE / M-2). Returns nil slice when the user has no saved methods.
func SelectPaymentMethodTokens(ctx context.Context, q Querier, userID uuid.UUID) ([]string, error) {
	rows, err := q.Query(ctx, selectPaymentMethodTokensSQL, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
	return tokens, rows.Err()
}

// DeletePaymentMethods removes the payment_methods rows (OQ-3 OVERRIDE — a
// deleted account must not retain a chargeable off-session token). Billing
// history is preserved independently by invoices (RETAIN-linked).
func DeletePaymentMethods(ctx context.Context, q Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, deletePaymentMethodsForUserSQL, userID)
	return err
}

// MarkAnonymizationComplete closes the BR-6.6 reconcile gate after the
// cross-store scrub succeeds.
func MarkAnonymizationComplete(ctx context.Context, q Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, markAnonymizationCompleteSQL, userID)
	return err
}
