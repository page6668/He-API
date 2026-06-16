// Story 2.7 — GDPR account deletion (30-day grace) RPC handlers.
//
//   - RequestAccountDeletion (AC2): re-auth-gated active→pending_deletion CAS,
//     server-computed NOW()+30d grace, session revocation, idempotent,
//     rate-limited; audits + fires the deletion-scheduled email.
//   - CancelAccountDeletion  (AC3): restorative pending→active CAS during grace
//     (no fresh re-auth — OQ-2); audits + fires the reactivated email.
//   - GetAccountDeletionState (AC1/AC3/AC4): read-only hydration driving the
//     dialog field branching + recovery countdown + console guard.
//
// user_id is ALWAYS the gateway-populated JWT sub (BR-2.4 IDOR defence) — the
// gateway proxy rejects any client body carrying user_id (400_invalid_body).
package handlers

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	passwordpkg "github.com/he-api/he-api/apps/auth-svc/internal/password"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

const (
	// AC2 BR-2.5 anti-accident rate limit: 5 requests / 24h per user.
	accountDeleteLimit  = 5
	accountDeleteWindow = 24 * time.Hour

	// BR-2.3 — the grace window is exactly 30 days (mirrored in the SQL
	// NOW()+INTERVAL '30 days'); the constant documents the contract for the
	// response's can_cancel_until == pending_deletion_at.
	accountDeleteGraceDays = 30

	// refreshRevokedKeyPrefix tombstones all refresh-token families minted for
	// a user before the marker's timestamp (BR-2.8). RefreshToken + the AC4
	// guard consult it; TTL matches the refresh-token lifetime so it self-expires
	// once no pre-deletion refresh token could still be valid.
	refreshRevokedKeyPrefix = "auth:refresh:revoked:"
)

// RequestAccountDeletion implements AC2.
func (s *AuthServer) RequestAccountDeletion(
	ctx context.Context,
	req *connect.Request[authv1.RequestAccountDeletionRequest],
) (*connect.Response[authv1.RequestAccountDeletionResponse], error) {
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	user, err := repository.GetUserByID(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			// JWT verified by the gateway but the row is gone (raced erasure) —
			// treat as unauthenticated, no enumeration.
			return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
		}
		return nil, internalErr(err, "pg_get_user_delete")
	}

	// Idempotency (BR-2.6): already pending → return the SAME pending_deletion_at
	// with NO re-auth, NO new audit/email, NO window extension.
	if user.Status == "pending_deletion" {
		if user.PendingDeletionAt == nil {
			return nil, internalErr(errors.New("pending_deletion without pending_deletion_at"), "delete_state_invariant")
		}
		return connect.NewResponse(deletionResponse(*user.PendingDeletionAt)), nil
	}
	// Non-active, non-pending (suspended/locked/deleted) → not deletable.
	if user.Status != "active" {
		return nil, statusError(connect.CodeFailedPrecondition, StatusAccountNotDeletable)
	}

	// Re-auth (BR-2.4, OWASP ASVS L2 V8.3) BEFORE consuming a rate-limit slot —
	// a wrong credential must NEVER mutate state and SHOULD NOT burn the
	// anti-accident budget. Empty/absent reauth fails closed (SEC-001 / BOUNDARY).
	if s.KMS == nil && user.TOTPEnabled {
		return nil, internalErr(errors.New("kms not configured"), "delete_deps_kms")
	}
	if !s.verifyDeletionReauth(ctx, user, in.GetReauth(), now) {
		return nil, statusError(connect.CodePermissionDenied, StatusBadReauth)
	}

	// Rate limit (BR-2.5). After re-auth so only genuine attempts count.
	rlKey := ratelimit.AccountDeleteKey(userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, accountDeleteLimit, accountDeleteWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		retryAfter := int(rl.RetryAfter/time.Second) + 1
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitAccountDelete, retryAfter)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_account_delete")
	}

	// Single-winner CAS active→pending_deletion (BR-2.7). Grace computed
	// server-side in SQL.
	pendingAt, matched, err := repository.RequestAccountDeletion(ctx, s.DB, userID)
	if err != nil {
		// CAS failed — release the just-consumed rate-limit slot so a transient
		// DB error doesn't permanently penalise the user (BLIND-DATA-002 / m-5).
		_ = s.Redis.Decr(ctx, rlKey).Err()
		return nil, internalErr(err, "pg_request_deletion")
	}
	if !matched {
		// Lost the CAS race (CONCURRENCY-001): a concurrent winner already set
		// pending_deletion. Re-read and return the idempotent existing record —
		// no new audit/email. Release our slot (no new state created).
		_ = s.Redis.Decr(ctx, rlKey).Err()
		cur, rErr := repository.GetUserByID(ctx, s.DB, userID)
		if rErr != nil {
			return nil, internalErr(rErr, "pg_reread_deletion")
		}
		if cur.Status == "pending_deletion" && cur.PendingDeletionAt != nil {
			return connect.NewResponse(deletionResponse(*cur.PendingDeletionAt)), nil
		}
		return nil, statusError(connect.CodeFailedPrecondition, StatusAccountNotDeletable)
	}

	// Revoke all prior sessions/refresh tokens (BR-2.8) — best-effort; the
	// status flip + fail-closed AC4 guard is the primary control (OQ-1).
	s.revokeAllUserSessions(ctx, userID, now)

	// Audit (PII-safe — BR-7.1/7.6) + deletion-scheduled email (BR best-effort).
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventAccountDeletionRequested,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":            audit.SeverityAccountDeletion(audit.EventAccountDeletionRequested),
			"pending_deletion_at": pendingAt.UTC().Format(time.RFC3339),
			"client_ip_hash":      oauthpkg.HashClientIP(in.GetClientIp()),
			"user_agent_hash":     oauthpkg.HashUserAgent(in.GetUserAgent()),
		},
	})
	if s.Notification != nil {
		if mErr := s.Notification.SendAccountDeletionEmail(ctx, notification.AccountDeletionRequested, user.Email, user.Locale, map[string]string{
			"display_name":        displayNameOrEmail(user),
			"pending_deletion_at": pendingAt.UTC().Format(time.RFC3339),
			"cancel_url":          s.ConsoleBaseURL + "/" + localeOrDefault(user.Locale) + "/account/recovery",
		}); mErr != nil {
			s.logWarn(ctx, "account_deletion_requested email send failed", "error", mErr.Error())
		}
	}

	return connect.NewResponse(deletionResponse(pendingAt)), nil
}

// CancelAccountDeletion implements AC3.
func (s *AuthServer) CancelAccountDeletion(
	ctx context.Context,
	req *connect.Request[authv1.CancelAccountDeletionRequest],
) (*connect.Response[authv1.CancelAccountDeletionResponse], error) {
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	// Restorative CAS pending→active (no fresh re-auth — OQ-2 / BR-3.4).
	matched, err := repository.CancelAccountDeletion(ctx, s.DB, userID)
	if err != nil {
		return nil, internalErr(err, "pg_cancel_deletion")
	}
	if !matched {
		// Disambiguate idempotent-already-active (200) vs grace-expired (410).
		cur, rErr := repository.GetUserByID(ctx, s.DB, userID)
		if rErr != nil {
			if errors.Is(rErr, repository.ErrUserNotFound) {
				return nil, statusError(connect.CodeFailedPrecondition, StatusGraceExpired)
			}
			return nil, internalErr(rErr, "pg_reread_cancel")
		}
		if cur.Status == "active" {
			// Already active — idempotent success, no second audit/email (BR-3.6).
			return connect.NewResponse(&authv1.CancelAccountDeletionResponse{Status: "active"}), nil
		}
		// deleted, or pending with an already-lapsed grace → the sweeper has run
		// or is due; recovery is no longer possible (BR-3.3).
		return nil, statusError(connect.CodeFailedPrecondition, StatusGraceExpired)
	}

	user, _ := repository.GetUserByID(ctx, s.DB, userID)
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventAccountDeletionCancelled,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":        audit.SeverityAccountDeletion(audit.EventAccountDeletionCancelled),
			"client_ip_hash":  oauthpkg.HashClientIP(in.GetClientIp()),
			"user_agent_hash": oauthpkg.HashUserAgent(in.GetUserAgent()),
		},
	})
	if s.Notification != nil && user != nil {
		if mErr := s.Notification.SendAccountDeletionEmail(ctx, notification.AccountDeletionCancelled, user.Email, user.Locale, map[string]string{
			"display_name": displayNameOrEmail(user),
		}); mErr != nil {
			s.logWarn(ctx, "account_deletion_cancelled email send failed", "error", mErr.Error())
		}
	}

	return connect.NewResponse(&authv1.CancelAccountDeletionResponse{Status: "active"}), nil
}

// GetAccountDeletionState implements the read-only hydration RPC (AC1/AC3/AC4).
func (s *AuthServer) GetAccountDeletionState(
	ctx context.Context,
	req *connect.Request[authv1.GetAccountDeletionStateRequest],
) (*connect.Response[authv1.GetAccountDeletionStateResponse], error) {
	userID, err := uuid.Parse(req.Msg.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}
	user, err := repository.GetUserByID(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
		}
		return nil, internalErr(err, "pg_get_user_state")
	}

	resp := &authv1.GetAccountDeletionStateResponse{
		Status:      user.Status,
		HasPassword: len(user.PasswordHash) > 0, // server-authoritative; hash NEVER leaves auth-svc
		TotpEnabled: user.TOTPEnabled,
		Timezone:    user.Timezone,
	}
	if user.Status == "pending_deletion" && user.PendingDeletionAt != nil {
		resp.PendingDeletionAt = timestamppb.New(*user.PendingDeletionAt)
	}
	return connect.NewResponse(resp), nil
}

// verifyDeletionReauth applies the AC1/AC2 re-auth matrix (BR-1.4 / BR-2.4):
// password-users → password (bcrypt); OAuth-only → exact confirm_email; ANY
// totp_enabled user → additionally a valid TOTP code. Fails closed on any
// absent/mismatched factor. reauth may be nil (empty payload → false).
func (s *AuthServer) verifyDeletionReauth(ctx context.Context, user *repository.User, reauth *authv1.ReauthCredentials, now time.Time) bool {
	if user.PasswordHash != nil {
		// Password-user: a non-empty password that bcrypt-matches.
		pw := reauth.GetPassword()
		if pw == "" || passwordpkg.Compare(user.PasswordHash, []byte(pw)) != nil {
			return false
		}
	} else {
		// OAuth-only: exact (normalized) confirm_email match.
		ce := reauth.GetConfirmEmail()
		if ce == "" {
			return false
		}
		norm, err := normalizeEmail(ce)
		if err != nil || norm != user.Email {
			return false
		}
	}
	// TOTP is an additional hard gate when enrolled (UNIT-014).
	if user.TOTPEnabled {
		code := reauth.GetTotpCode()
		if !isSixDigit(code) {
			return false
		}
		row, err := repository.GetTOTPSecret(ctx, s.DB, user.ID)
		if err != nil || row.EncryptedSecret == nil {
			return false
		}
		plain, dErr := s.KMS.Decrypt(ctx, row.EncryptedSecret)
		if dErr != nil {
			return false
		}
		if !totp.Validate(plain, code, now, totp.DefaultWindow) {
			return false
		}
	}
	return true
}

// revokeAllUserSessions writes the per-user refresh-revocation tombstone
// (BR-2.8). Best-effort: a Redis failure is logged but never blocks the
// already-committed deletion (the status flip + AC4 guard is the real control).
func (s *AuthServer) revokeAllUserSessions(ctx context.Context, userID uuid.UUID, now time.Time) {
	key := refreshRevokedKeyPrefix + userID.String()
	if err := s.Redis.Set(ctx, key, now.UTC().Format(time.RFC3339Nano), refreshTokenTTLSeconds()).Err(); err != nil {
		s.logWarn(ctx, "account_deletion session revoke marker failed", "error", err.Error())
	}
}

// deletionResponse builds the AC2 success body. can_cancel_until == the grace
// end (the whole window is cancelable — BR-3.x).
func deletionResponse(pendingAt time.Time) *authv1.RequestAccountDeletionResponse {
	ts := timestamppb.New(pendingAt)
	return &authv1.RequestAccountDeletionResponse{
		Status:            "pending_deletion",
		PendingDeletionAt: ts,
		CanCancelUntil:    ts,
	}
}

// displayNameOrEmail returns the user's display name, falling back to the email
// local-part — mirrors notification-svc resolveDisplayName so the email greeting
// is never blank. No PII beyond what the user already owns.
func displayNameOrEmail(u *repository.User) string {
	if u.DisplayName != nil && *u.DisplayName != "" {
		return *u.DisplayName
	}
	if at := indexByte(u.Email, '@'); at > 0 {
		return u.Email[:at]
	}
	return u.Email
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func localeOrDefault(locale string) string {
	if validLocales[locale] {
		return locale
	}
	return defaultLocale
}
