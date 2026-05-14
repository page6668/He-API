package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecoveryCodeRow is the projection used by UseRecoveryCode — only the id +
// hash columns are needed for the bcrypt-compare loop. Full forensic columns
// are read separately for audit reports.
type RecoveryCodeRow struct {
	ID       uuid.UUID
	CodeHash string
}

// SQL strings — pre-baked so QA static scans / grep can locate the queries.
const (
	insertRecoveryCodeSQL = `INSERT INTO he_api.mfa_recovery_codes (id, user_id, code_hash, created_at)
VALUES (gen_random_uuid(), $1, $2, NOW())`

	// listUnusedSQL drives UseRecoveryCode. SELECT … FOR UPDATE so two
	// concurrent challenges with the same user cannot both mark the same
	// row as used (BR-3.3). idx_mfa_recovery_user_unused carries the lookup.
	listUnusedSQL = `SELECT id, code_hash FROM he_api.mfa_recovery_codes
WHERE user_id = $1 AND used_at IS NULL
FOR UPDATE`

	// markUsedSQL is the atomic mark-used UPDATE with row-count check.
	// Returning rowCount=0 means another concurrent operation already
	// claimed this row — treat as no-match per BR-3.3 race protection.
	markUsedSQL = `UPDATE he_api.mfa_recovery_codes
SET used_at=NOW(), used_from_ip_hash=$2, used_from_ua_hash=$3
WHERE id=$1 AND used_at IS NULL`

	// markAllUnusedSQL is the AC3 RegenerateRecoveryCodes bulk-mark step
	// (forensic trail preserved; physically retained per BR-3.5).
	markAllUnusedSQL = `UPDATE he_api.mfa_recovery_codes
SET used_at=NOW(), regenerated_at=NOW(),
    regenerated_reason=$2, used_from_ip_hash=$3, used_from_ua_hash=$4
WHERE user_id=$1 AND used_at IS NULL`

	// deleteAllForUserSQL is the AC4 Disable physical-delete (clean slate
	// per BR-3.9 / BR-4.3). Foreign-key ON DELETE CASCADE from users
	// covers the Story 2.7 account-deletion path independently.
	deleteAllForUserSQL = `DELETE FROM he_api.mfa_recovery_codes WHERE user_id=$1`

	// countUnusedSQL drives the AC3 low-codes banner (BR-3.7). Index-only
	// scan via idx_mfa_recovery_user_unused.
	countUnusedSQL = `SELECT COUNT(*) FROM he_api.mfa_recovery_codes
WHERE user_id=$1 AND used_at IS NULL`
)

// BulkInsertRecoveryCodes inserts `hashes` rows for `userID` in a single
// batch. Must be invoked inside the AC1 EnrollTOTPVerify (or AC3 Regenerate)
// transaction so partial inserts cannot leave the user with <10 codes.
//
// pgx auto-batches when SendBatch is used; here we issue 10 individual
// Execs because the batch ergonomics are heavier than the win (10 rows
// max). For larger sets prefer COPY FROM.
func BulkInsertRecoveryCodes(ctx context.Context, q Querier, userID uuid.UUID, hashes []string) error {
	for _, h := range hashes {
		if _, err := q.Exec(ctx, insertRecoveryCodeSQL, userID, h); err != nil {
			return err
		}
	}
	return nil
}

// ListUnusedRecoveryCodes returns id + hash for every unused row. Caller
// MUST be inside a transaction to honor the SELECT … FOR UPDATE row lock.
func ListUnusedRecoveryCodes(ctx context.Context, q Querier, userID uuid.UUID) ([]RecoveryCodeRow, error) {
	rows, err := q.Query(ctx, listUnusedSQL, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecoveryCodeRow
	for rows.Next() {
		var r RecoveryCodeRow
		if err := rows.Scan(&r.ID, &r.CodeHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkRecoveryCodeUsed atomically marks the supplied row as used. Returns
// (true, nil) when exactly one row was updated; (false, nil) on a race
// where another concurrent operation already claimed the row (BR-3.3).
func MarkRecoveryCodeUsed(ctx context.Context, q Querier, codeID uuid.UUID, ipHash, uaHash string) (bool, error) {
	tag, err := q.Exec(ctx, markUsedSQL, codeID, ipHash, uaHash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkAllRecoveryCodesUnused is the AC3 Regenerate bulk-mark step. Preserves
// forensic trail (no DELETE) per BR-3.5.
func MarkAllRecoveryCodesUnused(ctx context.Context, q Querier, userID uuid.UUID, reason, ipHash, uaHash string) error {
	_, err := q.Exec(ctx, markAllUnusedSQL, userID, reason, ipHash, uaHash)
	return err
}

// DeleteAllRecoveryCodesForUser is the AC4 Disable physical-delete step
// (BR-3.9 clean slate).
func DeleteAllRecoveryCodesForUser(ctx context.Context, q Querier, userID uuid.UUID) error {
	_, err := q.Exec(ctx, deleteAllForUserSQL, userID)
	return err
}

// CountUnusedRecoveryCodes returns the unused count for the low-codes banner
// (BR-3.7).
func CountUnusedRecoveryCodes(ctx context.Context, q Querier, userID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, countUnusedSQL, userID).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Unused — silence the time-import linter (added defensively for future
// reporting helpers that may want to filter on created_at windows).
var _ = time.Time{}

// Ensure errors package linkage compiles cleanly without dragging the
// import for the simple operations above.
var _ = pgx.ErrNoRows
