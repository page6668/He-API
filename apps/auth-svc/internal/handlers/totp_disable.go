// Story 2.4 AC4 — DisableTOTP handler.
//
// Flow:
//
//  1. Validate user_id + factor + value.
//  2. Rate-limit ratelimit:2fa:disable:{user_id} (3 failures/hour per BR-4.4).
//  3. Load user → reject 409_not_enrolled when totp_enabled=FALSE.
//  4. Factor verify (TOTP via KMS decrypt + RFC 6238 ±1 / password via bcrypt).
//  5. Single PG transaction (s.DBTx.Begin) — UPDATE users SET totp_*=NULL/false
//     + DELETE FROM mfa_recovery_codes WHERE user_id=$1 (BR-3.9 + BR-4.3 —
//     atomic clean slate; partial failure rolls back both statements so the
//     user never lands in totp_enabled=FALSE with orphan recovery rows).
//  6. notification-svc SendSecurityAlert(Alert2FADisabled) — HIGH severity
//     per BR-4.7 / BR-5.9.
//  7. Audit `2fa.disabled` HIGH (severity routing in audit-svc).
//
// BR-4.1 (aal=2 session) is enforced at the api-gateway aal_check.go
// middleware (T5.2). auth-svc trusts that gateway check; we re-verify the
// factor regardless as defense-in-depth per BR-4.2.
package handlers

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/metrics"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
)

// DisableTOTP implements AC4.
func (s *AuthServer) DisableTOTP(
	ctx context.Context,
	req *connect.Request[authv1.DisableTOTPRequest],
) (*connect.Response[authv1.DisableTOTPResponse], error) {
	if s.KMS == nil {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps_disable")
	}
	if s.DBTx == nil {
		return nil, internalErr(errors.New("tx-capable DB not configured"), "mfa_deps_disable_tx")
	}
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	// Factor enum validation.
	switch in.GetFactor() {
	case authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD:
	default:
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidFactor)
	}

	// Rate limit (BR-4.4 — 3 failures/hour, separate counter from challenge).
	rlKey := ratelimit.MFAKey(ratelimit.OpMFADisable, userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, rl2FADisableLimit, rl2FADisableWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		// 1h soft-lock; mirrors AC3 recovery exhaustion path.
		until := now.Add(time.Hour)
		_ = repository.SoftLockUser(ctx, s.DB, userID, until)
		s.Metrics.Inc2FALocked(ctx, metrics.TwoFAFactorDisable)
		retryAfter := int(rl.RetryAfter/time.Second) + 1
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusAccountLocked, retryAfter)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_disable")
	}

	// Load user.
	user, err := repository.GetUserByID(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, statusError(connect.CodeUnauthenticated, StatusInvalidUserID)
		}
		return nil, internalErr(err, "pg_get_user_disable")
	}
	if !user.TOTPEnabled {
		return nil, statusError(connect.CodeFailedPrecondition, StatusNotEnrolled)
	}

	// Factor verify (defense-in-depth per BR-4.2; gateway also enforces aal=2).
	if !s.verifyRegenerateFactor(ctx, &authv1.RegenerateRecoveryCodesRequest{
		UserId: userID.String(),
		Factor: in.GetFactor(),
		Value:  in.GetValue(),
	}, user, now) {
		// Audit failure but do NOT distinguish wrong-totp vs wrong-password
		// in the response (BR-5.10 spirit applied — consistent error shape).
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeFailed,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusInvalidCredentials,
			Metadata: map[string]any{
				"severity":   audit.SeverityLow,
				"factor":     factorString(in.GetFactor()),
				"disable_op": true,
			},
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}

	// PG state mutations — single transaction so a partial failure rolls
	// back both statements (AC4 Scenario / BR-3.9 — clean slate atomicity;
	// QA-2.4-H1 fix).
	tx, err := s.DBTx.Begin(ctx)
	if err != nil {
		return nil, internalErr(err, "pg_tx_begin_disable")
	}
	// Defer Rollback as a safety net; once Commit succeeds, Rollback becomes
	// a no-op per pgx.Tx contract (returns pgx.ErrTxClosed which we discard).
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := repository.ClearTOTPSecret(ctx, tx, userID); err != nil {
		return nil, internalErr(err, "pg_clear_totp")
	}
	if err := repository.DeleteAllRecoveryCodesForUser(ctx, tx, userID); err != nil {
		return nil, internalErr(err, "pg_delete_recovery")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, internalErr(err, "pg_tx_commit_disable")
	}

	// Clear the disable failure counter on success — user proved factor.
	_ = s.Redis.Del(ctx, rlKey).Err()

	// HIGH-severity out-of-band email (BR-4.5 + BR-5.9). The disable method
	// is included so the user can tell from the email whether they (or an
	// attacker) authorized this with TOTP or password.
	if s.Notification != nil {
		alertErr := s.Notification.SendSecurityAlert(ctx, notification.Alert2FADisabled, user.Email, user.Locale, map[string]string{
			"time":           now.UTC().Format(time.RFC3339),
			"ip_summary":     maskIP(in.GetClientIp()),
			"ua_summary":     summarizeUA(in.GetUserAgent()),
			"disable_method": factorString(in.GetFactor()),
		})
		if alertErr != nil {
			s.logWarn(ctx, "2fa_disabled email send failed", "error", alertErr.Error())
		}
	}

	// HIGH audit per BR-4.7 / BR-5.7.
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FADisabled,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":        audit.SeverityHigh,
			"disable_method":  factorString(in.GetFactor()),
			"ip_hash":         oauthpkg.HashClientIP(in.GetClientIp()),
			"ua_hash":         oauthpkg.HashUserAgent(in.GetUserAgent()),
		},
	})
	switch in.GetFactor() {
	case authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP:
		s.Metrics.Inc2FADisabled(ctx, metrics.TwoFAFactorTOTP)
	case authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD:
		s.Metrics.Inc2FADisabled(ctx, metrics.TwoFAFactorPassword)
	}

	return connect.NewResponse(&authv1.DisableTOTPResponse{Ok: true}), nil
}
