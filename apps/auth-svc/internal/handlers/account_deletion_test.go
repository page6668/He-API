package handlers_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v3"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/handlers"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
)

// casRows returns a single-column pending_deletion_at result for the AC2 CAS.
func casRows(at time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"pending_deletion_at"}).AddRow(at)
}

// expectGetUser stubs the GetUserByID read with the supplied shape.
func expectGetUser(h *harness, userID uuid.UUID, pwHash []byte, totpEnabled bool, status string, pendingAt *time.Time, oauthProvider *string) {
	verifiedAt := time.Now().Add(-time.Hour)
	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			oauthProvider, nil, "en", "UTC", nil, totpEnabled, status, nil, pendingAt,
			time.Now(), time.Now(),
		))
}

// --- AC2: RequestAccountDeletion -----------------------------------------

// 2.7-UNIT-011 / INT-006 / INT-010 / UNIT-016 — password-user happy path:
// bcrypt match → active→pending CAS (NOW()+30d) → audit + SendEmail(=requested).
func TestRequestAccountDeletion_PasswordHappyPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	pwHash := mustHash(t, "correct horse battery staple")
	pendingAt := fixedNow.Add(30 * 24 * time.Hour)

	expectGetUser(h, userID, pwHash, false, "active", nil, nil)
	// CAS — the SQL regex pins NOW()+INTERVAL '30 days' (BR-2.3 / UNIT-016).
	h.mock.ExpectQuery(`UPDATE he_api\.users\s+SET status='pending_deletion', pending_deletion_at = NOW\(\) \+ INTERVAL '30 days'`).
		WithArgs(userID).
		WillReturnRows(casRows(pendingAt))

	resp, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId:    userID.String(),
		Reauth:    &authv1.ReauthCredentials{Password: ptr("correct horse battery staple")},
		ClientIp:  "1.2.3.4",
		UserAgent: "Mozilla/5.0",
	}))
	if err != nil {
		t.Fatalf("RequestAccountDeletion: %v", err)
	}
	if resp.Msg.GetStatus() != "pending_deletion" {
		t.Errorf("status = %q, want pending_deletion", resp.Msg.GetStatus())
	}
	if !resp.Msg.GetPendingDeletionAt().AsTime().Equal(pendingAt) {
		t.Errorf("pending_deletion_at = %v, want %v", resp.Msg.GetPendingDeletionAt().AsTime(), pendingAt)
	}
	// can_cancel_until == pending_deletion_at (whole window cancelable).
	if !resp.Msg.GetCanCancelUntil().AsTime().Equal(pendingAt) {
		t.Errorf("can_cancel_until != pending_deletion_at")
	}
	if got := h.auditP.byType(audit.EventAccountDeletionRequested); len(got) != 1 {
		t.Fatalf("requested audits = %d, want 1", len(got))
	} else if sev, _ := got[0].Metadata["severity"].(string); sev != audit.SeverityMedium {
		t.Errorf("severity = %q, want MEDIUM", sev)
	}
	if h.notif.delCalls != 1 || h.notif.lastDelTmpl != notification.AccountDeletionRequested {
		t.Errorf("deletion email calls=%d tmpl=%v, want 1/Requested", h.notif.delCalls, h.notif.lastDelTmpl)
	}
	// Session-revocation tombstone written (BR-2.8).
	if !h.mr.Exists("auth:refresh:revoked:" + userID.String()) {
		t.Errorf("session revocation marker not set")
	}
}

// 2.7-UNIT-012 / SEC-001 — wrong password → 403_bad_reauth, NO state change.
func TestRequestAccountDeletion_WrongPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	expectGetUser(h, userID, mustHash(t, "the real password"), false, "active", nil, nil)
	// NO CAS expectation — a wrong credential must not mutate state.

	_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{Password: ptr("WRONG")},
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusBadReauth)
	if h.notif.delCalls != 0 || len(h.auditP.byType(audit.EventAccountDeletionRequested)) != 0 {
		t.Errorf("wrong password must not audit/email")
	}
}

// 2.7-BLIND-BOUNDARY-001 — empty reauth object → 403, no state change.
func TestRequestAccountDeletion_EmptyReauth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	expectGetUser(h, userID, mustHash(t, "pw"), false, "active", nil, nil)

	_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{}, // all blank
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusBadReauth)
}

// 2.7-UNIT-013 — OAuth-only user: exact confirm_email match required.
func TestRequestAccountDeletion_OAuthOnlyConfirmEmail(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	google := "google"
	pendingAt := fixedNow.Add(30 * 24 * time.Hour)

	// password_hash NULL → OAuth-only; mismatch first.
	expectGetUser(h, userID, nil, false, "active", nil, &google)
	_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{ConfirmEmail: ptr("WRONG@example.com")},
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusBadReauth)

	// exact match proceeds.
	expectGetUser(h, userID, nil, false, "active", nil, &google)
	h.mock.ExpectQuery(`UPDATE he_api\.users\s+SET status='pending_deletion'`).
		WithArgs(userID).WillReturnRows(casRows(pendingAt))
	resp, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{ConfirmEmail: ptr("user@example.com")},
	}))
	if err != nil {
		t.Fatalf("oauth confirm match: %v", err)
	}
	if resp.Msg.GetStatus() != "pending_deletion" {
		t.Errorf("status = %q", resp.Msg.GetStatus())
	}
}

// 2.7-INT-007 / CONCURRENCY-001 — idempotent: already pending → 200 same
// pending_deletion_at, NO new audit/email.
func TestRequestAccountDeletion_IdempotentAlreadyPending(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	existing := fixedNow.Add(20 * 24 * time.Hour)
	expectGetUser(h, userID, mustHash(t, "pw"), false, "pending_deletion", &existing, nil)
	// NO CAS, NO reauth needed on the idempotent path.

	resp, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{}, // ignored on idempotent path
	}))
	if err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if !resp.Msg.GetPendingDeletionAt().AsTime().Equal(existing) {
		t.Errorf("pending_deletion_at = %v, want existing %v", resp.Msg.GetPendingDeletionAt().AsTime(), existing)
	}
	if h.notif.delCalls != 0 || len(h.auditP.byType(audit.EventAccountDeletionRequested)) != 0 {
		t.Errorf("idempotent path must not re-audit/re-email")
	}
}

// 2.7-INT-008 — suspended/locked/deleted → 409_account_not_deletable.
func TestRequestAccountDeletion_NotDeletableStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"suspended", "locked", "deleted"} {
		status := status
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			userID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
			expectGetUser(h, userID, mustHash(t, "pw"), false, status, nil, nil)
			_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
				UserId: userID.String(),
				Reauth: &authv1.ReauthCredentials{Password: ptr("pw")},
			}))
			assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusAccountNotDeletable)
		})
	}
}

// 2.7-INT-011 — rate-limit > 5/24h → 429, no orphan pending state.
func TestRequestAccountDeletion_RateLimited(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	expectGetUser(h, userID, mustHash(t, "pw"), false, "active", nil, nil)
	// Pre-fill the counter at the ceiling so the handler's INCR trips.
	_ = h.mr.Set("ratelimit:account:delete:"+userID.String(), "5")
	// NO CAS — rate-limited before the transition.

	_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{Password: ptr("pw")},
	}))
	assertConnectStatus(t, err, connect.CodeResourceExhausted, handlers.StatusRateLimitAccountDelete)
}

// 2.7-UNIT-014 — totp_enabled user: wrong TOTP → 403 even with correct password.
func TestRequestAccountDeletion_WrongTOTP(t *testing.T) {
	t.Parallel()
	h := newChallengeHarness(t)
	userID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	pwHash := mustHash(t, "correct horse battery staple")
	secret := []byte("12345678901234567890")
	enc := base64Encode(secret)
	verifiedAt := time.Now().Add(-time.Hour)

	h.mock.ExpectQuery(`SELECT .* FROM he_api\.users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(userRows().AddRow(
			userID, "user@example.com", pwHash, &verifiedAt,
			nil, nil, "en", "UTC", &enc, true, "active", nil, nil,
			time.Now(), time.Now(),
		))
	h.mock.ExpectQuery(`SELECT totp_secret_encrypted, totp_enabled, totp_enrolled_at FROM he_api\.users WHERE id=\$1`).
		WithArgs(userID).
		WillReturnRows(totpUserRow(true, &enc))

	_, err := h.srv.RequestAccountDeletion(context.Background(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId: userID.String(),
		Reauth: &authv1.ReauthCredentials{Password: ptr("correct horse battery staple"), TotpCode: ptr("000000")},
	}))
	assertConnectStatus(t, err, connect.CodePermissionDenied, handlers.StatusBadReauth)
}

// --- AC3: CancelAccountDeletion ------------------------------------------

// 2.7-UNIT-017 / INT-012 / UNIT-020 — cancel CAS pending→active (only JWT, no
// fresh re-auth) → audit cancelled + SendEmail(=cancelled) + 200 {active}.
func TestCancelAccountDeletion_HappyPath(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("99999999-9999-9999-9999-999999999999")
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET status='active', pending_deletion_at=NULL.*WHERE id=\$1 AND status='pending_deletion' AND pending_deletion_at > NOW\(\)`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Post-cancel re-read for the email greeting.
	expectGetUser(h, userID, mustHash(t, "pw"), false, "active", nil, nil)

	resp, err := h.srv.CancelAccountDeletion(context.Background(), connect.NewRequest(&authv1.CancelAccountDeletionRequest{
		UserId: userID.String(),
	}))
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if resp.Msg.GetStatus() != "active" {
		t.Errorf("status = %q, want active", resp.Msg.GetStatus())
	}
	if got := h.auditP.byType(audit.EventAccountDeletionCancelled); len(got) != 1 {
		t.Errorf("cancelled audits = %d, want 1", len(got))
	}
	if h.notif.delCalls != 1 || h.notif.lastDelTmpl != notification.AccountDeletionCancelled {
		t.Errorf("cancel email calls=%d tmpl=%v", h.notif.delCalls, h.notif.lastDelTmpl)
	}
}

// 2.7-UNIT-018 — grace expired / deleted (0 rows + status deleted) → 410.
func TestCancelAccountDeletion_GraceExpired(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET status='active'`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	expectGetUser(h, userID, nil, false, "deleted", nil, nil)

	_, err := h.srv.CancelAccountDeletion(context.Background(), connect.NewRequest(&authv1.CancelAccountDeletionRequest{
		UserId: userID.String(),
	}))
	assertConnectStatus(t, err, connect.CodeFailedPrecondition, handlers.StatusGraceExpired)
}

// 2.7-UNIT-019 — idempotent: cancel an already-active account → 200, no 2nd email.
func TestCancelAccountDeletion_IdempotentActive(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	h.mock.ExpectExec(`UPDATE he_api\.users\s+SET status='active'`).
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	expectGetUser(h, userID, mustHash(t, "pw"), false, "active", nil, nil)

	resp, err := h.srv.CancelAccountDeletion(context.Background(), connect.NewRequest(&authv1.CancelAccountDeletionRequest{
		UserId: userID.String(),
	}))
	if err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if resp.Msg.GetStatus() != "active" {
		t.Errorf("status = %q, want active", resp.Msg.GetStatus())
	}
	if h.notif.delCalls != 0 {
		t.Errorf("idempotent cancel must not email")
	}
}

// --- GetAccountDeletionState ---------------------------------------------

// has_password / totp_enabled / status / pending_deletion_at hydration.
func TestGetAccountDeletionState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	userID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	pendingAt := fixedNow.Add(10 * 24 * time.Hour)
	expectGetUser(h, userID, mustHash(t, "pw"), true, "pending_deletion", &pendingAt, nil)

	resp, err := h.srv.GetAccountDeletionState(context.Background(), connect.NewRequest(&authv1.GetAccountDeletionStateRequest{
		UserId: userID.String(),
	}))
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !resp.Msg.GetHasPassword() || !resp.Msg.GetTotpEnabled() {
		t.Errorf("has_password/totp_enabled wrong: %v/%v", resp.Msg.GetHasPassword(), resp.Msg.GetTotpEnabled())
	}
	if resp.Msg.GetStatus() != "pending_deletion" {
		t.Errorf("status = %q", resp.Msg.GetStatus())
	}
	if !resp.Msg.GetPendingDeletionAt().AsTime().Equal(pendingAt) {
		t.Errorf("pending_deletion_at = %v, want %v", resp.Msg.GetPendingDeletionAt().AsTime(), pendingAt)
	}
}

func ptr(s string) *string { return &s }
