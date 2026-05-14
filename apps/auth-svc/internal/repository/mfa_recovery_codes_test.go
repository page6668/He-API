package repository_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// Scenario: 2.4-BLIND-DATA-001 (QA Round 1 M4 mitigation) — recovery code
// forensic trail. MarkRecoveryCodeUsed MUST:
//
//   - write used_at=NOW(), used_from_ip_hash=$2, used_from_ua_hash=$3
//     (so post-incident forensics can identify the request that consumed
//     the code; BR-3.5);
//   - guard with `used_at IS NULL` in the WHERE clause (idempotent; a
//     re-attempt against an already-used row returns RowsAffected=0 and
//     surfaces as `(false, nil)` to the caller — race-safe per BR-3.3).
//
// This test pins the SQL contract via pgxmock regex matching — a future
// refactor that drops a forensic column or omits the idempotent guard
// will fail the regex and surface the regression at PR time.
func TestMarkRecoveryCodeUsed_WritesForensicColumns(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	codeID := uuid.New()
	ipHash := "ip-hash-abc"
	uaHash := "ua-hash-xyz"

	mock.ExpectExec(`(?s)UPDATE he_api\.mfa_recovery_codes\s+SET used_at=NOW\(\),\s*used_from_ip_hash=\$2,\s*used_from_ua_hash=\$3\s+WHERE id=\$1 AND used_at IS NULL`).
		WithArgs(codeID, ipHash, uaHash).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ok, err := repository.MarkRecoveryCodeUsed(context.Background(), mock, codeID, ipHash, uaHash)
	if err != nil {
		t.Fatalf("MarkRecoveryCodeUsed: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true (1 row updated)")
	}
}

// Scenario: 2.4-BLIND-DATA-001 (race branch) — when a concurrent operation
// already marked the row used (RowsAffected=0 because WHERE used_at IS NULL
// no longer matches), MarkRecoveryCodeUsed returns `(false, nil)` instead
// of erroring — BR-3.3 race-safety contract for the UseRecoveryCode handler.
func TestMarkRecoveryCodeUsed_ReturnsFalseOnRaceLoss(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	codeID := uuid.New()

	mock.ExpectExec(`UPDATE he_api\.mfa_recovery_codes\s+SET used_at=NOW`).
		WithArgs(codeID, "ip", "ua").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	ok, err := repository.MarkRecoveryCodeUsed(context.Background(), mock, codeID, "ip", "ua")
	if err != nil {
		t.Fatalf("err = %v, want nil on race-loss", err)
	}
	if ok {
		t.Fatalf("ok = true, want false (0 rows updated → race lost)")
	}
}

// Scenario: 2.4-BLIND-DATA-001 (regenerate path) — MarkAllRecoveryCodesUnused
// preserves the forensic trail by NOT deleting rows; it writes used_at=NOW()
// + regenerated_at=NOW() + regenerated_reason + used_from_ip_hash/used_from_ua_hash
// across every unused row for the user. The SQL contract is pinned here.
func TestMarkAllRecoveryCodesUnused_PreservesForensicTrailViaBulkMark(t *testing.T) {
	t.Parallel()
	mock := newMock(t)
	userID := uuid.New()

	mock.ExpectExec(`(?s)UPDATE he_api\.mfa_recovery_codes\s+SET used_at=NOW\(\),\s*regenerated_at=NOW\(\),\s*regenerated_reason=\$2,\s*used_from_ip_hash=\$3,\s*used_from_ua_hash=\$4\s+WHERE user_id=\$1 AND used_at IS NULL`).
		WithArgs(userID, "user_requested", "ip-hash", "ua-hash").
		WillReturnResult(pgxmock.NewResult("UPDATE", 7))

	err := repository.MarkAllRecoveryCodesUnused(context.Background(), mock, userID, "user_requested", "ip-hash", "ua-hash")
	if err != nil {
		t.Fatalf("MarkAllRecoveryCodesUnused: %v", err)
	}
}
