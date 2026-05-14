// Story 2.4 AC3 — Recovery codes (UseRecoveryCode + RegenerateRecoveryCodes).
//
// UseRecoveryCode shares the mfa_token + JTI + IP/UA binding scaffolding with
// ChallengeTOTP. The point of difference: instead of validating a 6-digit code
// against a TOTP secret, it bcrypt-compares against the user's unused
// mfa_recovery_codes rows + atomic mark-used.
//
// **Constant-time invariant** (BR-3.6 / Architect §Rec.1): even after a
// match, the loop iterates ALL 10 unused rows so latency does not reveal
// match position. We use a sequential loop (cost=12 × 10 ≈ 250-300ms median
// per the Architect Q4 staging benchmark gate — within the 200ms p95 budget
// per the Q4 caveat that allows fall-back to cost=10).
//
// RegenerateRecoveryCodes lives in this file too; both endpoints emit
// HIGH-severity audit + an out-of-band security-alert email per BR-3.8 / BR-5.9.

package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/metrics"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	passwordpkg "github.com/he-api/he-api/apps/auth-svc/internal/password"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/recovery"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

// lowCodesThreshold is the BR-3.7 threshold for `recovery_codes_low=true` in
// UseRecoveryCodeResponse. Console surfaces a persistent dashboard banner
// when this trips.
const lowCodesThreshold = 3

// UseRecoveryCode implements AC3 — validates a single-use recovery code at
// the 2FA challenge step (in place of the TOTP 6-digit input), promotes the
// session to aal=2, sends an out-of-band security-alert email.
func (s *AuthServer) UseRecoveryCode(
	ctx context.Context,
	req *connect.Request[authv1.UseRecoveryCodeRequest],
) (*connect.Response[authv1.UseRecoveryCodeResponse], error) {
	if s.MFAParser == nil || s.JWT == nil {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps_recovery")
	}
	in := req.Msg
	now := s.Clock()

	// === 1. Parse + verify mfa_token (mirrors ChallengeTOTP step 1). ===
	tok := strings.TrimSpace(in.GetMfaToken())
	if tok == "" {
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}
	claims, err := s.MFAParser.ParseMFAToken(tok)
	if err != nil {
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}

	// === 2. Redis JTI exists (BR-5.4). ===
	jtiKey := challengeJTIKey(claims.JTI)
	exists, err := s.Redis.Exists(ctx, jtiKey).Result()
	if err != nil {
		return nil, internalErr(err, "redis_jti_get_recovery")
	}
	if exists == 0 {
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}

	// === 3. IP/UA binding (BR-2.3 reused). ===
	curIP := oauthpkg.HashClientIP(in.GetClientIp())
	curUA := oauthpkg.HashUserAgent(in.GetUserAgent())
	if curIP != claims.IPHash || curUA != claims.UserAgentHash {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeBinding,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusMFATokenBindingMismatch,
			Metadata:  map[string]any{"severity": audit.SeverityHigh, "factor": "recovery"},
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenBindingMismatch)
	}

	// === 4. Normalize the code (strip hyphens + uppercase + length check). ===
	normalized, err := recovery.Normalize(in.GetCode())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidRecoveryCodeFormat)
	}

	// === 5. Rate limit (3 failures/15min per BR-3.6 — stricter than TOTP). ===
	rlKey := ratelimit.MFAKey(ratelimit.OpMFARecovery, userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, rl2FARecoveryLimit, rl2FARecoveryWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		until := now.Add(time.Hour)
		_ = repository.SoftLockUser(ctx, s.DB, userID, until)
		_ = s.Redis.Del(ctx, jtiKey).Err()
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeLocked,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusAccountLocked,
			Metadata:  map[string]any{"severity": audit.SeverityHigh, "factor": "recovery"},
		})
		s.Metrics.Inc2FALocked(ctx, metrics.TwoFAFactorRecovery)
		retryAfter := int(rl.RetryAfter/time.Second) + 1
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusAccountLocked, retryAfter)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_recovery")
	}

	// === 6. Load all unused codes (FOR UPDATE row lock per BR-3.3). ===
	rows, err := repository.ListUnusedRecoveryCodes(ctx, s.DB, userID)
	if err != nil {
		return nil, internalErr(err, "pg_list_unused_recovery")
	}
	if len(rows) == 0 {
		// No unused codes at all — distinct error so UI can prompt regenerate.
		return nil, statusError(connect.CodeFailedPrecondition, StatusNoRecoveryCodes)
	}

	// === 7. Constant-time bcrypt-compare across ALL rows (BR-3.6 invariant). ===
	// Iterate to the end even after a match; record the matched row id.
	matchedID := uuid.Nil
	for _, r := range rows {
		if recovery.Compare(r.CodeHash, normalized) && matchedID == uuid.Nil {
			matchedID = r.ID
			// Do NOT break — preserve constant-time iteration.
		}
	}
	if matchedID == uuid.Nil {
		// No row matched — counter increments.
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeFailed,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusInvalidRecoveryCode,
			Metadata:  map[string]any{"severity": audit.SeverityLow, "factor": "recovery"},
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidRecoveryCode)
	}

	// === 8. Atomic mark-used (BR-3.3 — row count = 0 on race). ===
	ipHash := curIP
	uaHash := curUA
	updated, err := repository.MarkRecoveryCodeUsed(ctx, s.DB, matchedID, ipHash, uaHash)
	if err != nil {
		return nil, internalErr(err, "pg_mark_recovery_used")
	}
	if !updated {
		// Another concurrent challenge already claimed this row. Treat as
		// no-match (constant-time invariant preserved).
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidRecoveryCode)
	}

	// === 9. Count remaining unused (BR-3.7 low-codes signal). ===
	remaining, err := repository.CountUnusedRecoveryCodes(ctx, s.DB, userID)
	if err != nil {
		s.logWarn(ctx, "count unused recovery failed (non-fatal)", "error", err.Error())
		remaining = -1
	}

	// === 10. Issue access+refresh + DEL JTI + audit + email. ===
	if err := s.Redis.Del(ctx, jtiKey).Err(); err != nil {
		s.logWarn(ctx, "DEL jti after recovery success failed", "error", err.Error())
	}
	_ = repository.MarkTOTPUsed(ctx, s.DB, userID)

	familyID, err := uuid.NewRandom()
	if err != nil {
		return nil, internalErr(err, "gen_family_id_recovery")
	}
	accessToken, err := s.JWT.SignAccessTokenWithAAL(userID, now, 2)
	if err != nil {
		return nil, internalErr(err, "sign_access_recovery")
	}
	refreshToken, err := s.JWT.SignRefreshToken(userID, familyID, now)
	if err != nil {
		return nil, internalErr(err, "sign_refresh_recovery")
	}
	refreshJTI, err := jtiFromToken(refreshToken)
	if err != nil {
		return nil, internalErr(err, "decode_refresh_jti_recovery")
	}
	familyKey := refreshFamilyKeyPrefix + familyID.String()
	if err := s.Redis.Set(ctx, familyKey, refreshJTI, refreshTokenTTLSeconds()).Err(); err != nil {
		return nil, internalErr(err, "redis_refresh_family_recovery")
	}

	// Clear the recovery counter on success.
	_ = s.Redis.Del(ctx, rlKey).Err()

	// Out-of-band email (BR-3.8 — every recovery use is a security event).
	if s.Notification != nil {
		alertErr := s.Notification.SendSecurityAlert(ctx, notification.Alert2FARecoveryUsed, "", "", map[string]string{
			"time":            now.UTC().Format(time.RFC3339),
			"ip_summary":      maskIP(in.GetClientIp()),
			"ua_summary":      summarizeUA(in.GetUserAgent()),
			"remaining_count": itoaInt(remaining),
		})
		if alertErr != nil {
			s.logWarn(ctx, "2fa_recovery_used email send failed", "error", alertErr.Error())
		}
	}

	// HIGH-severity audit per BR-5.7.
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FARecoveryUsed,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":         audit.SeverityHigh,
			"remaining_count":  remaining,
			"login_method":     claims.LoginMethod,
		},
	})
	s.Metrics.IncSignin(ctx, metrics.SigninResultSuccess, "recovery_code")
	s.Metrics.Inc2FARecoveryUsed(ctx)
	s.Metrics.Inc2FAChallenge(ctx, metrics.TwoFAResultSuccess, metrics.TwoFAFactorRecovery)

	return connect.NewResponse(&authv1.UseRecoveryCodeResponse{
		AccessToken:                  accessToken,
		RefreshToken:                 refreshToken,
		AccessTokenExpiresInSeconds:  int32(accessTokenTTLSeconds().Seconds()),
		RefreshTokenExpiresInSeconds: int32(refreshTokenTTLSeconds().Seconds()),
		Aal:                          2,
		RecoveryCodesRemaining:       int32(remaining),
		RecoveryCodesLow:             remaining >= 0 && remaining <= lowCodesThreshold,
		ReturnTo:                     claims.ReturnTo,
	}), nil
}

// RegenerateRecoveryCodes implements AC3 — verify factor (TOTP or password),
// bulk-mark all current unused codes as used (forensic trail per BR-3.5),
// issue 10 fresh codes, return plaintext ONCE.
//
// JWT-required (aal=2) — enforced at the api-gateway aal_check.go middleware
// (T5.2). Server-side we trust the gateway here; the verify-factor step is
// the second authentication factor (defense-in-depth per BR-4.2 spirit
// applied to AC3 regenerate too).
func (s *AuthServer) RegenerateRecoveryCodes(
	ctx context.Context,
	req *connect.Request[authv1.RegenerateRecoveryCodesRequest],
) (*connect.Response[authv1.RegenerateRecoveryCodesResponse], error) {
	if s.KMS == nil {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps_regenerate")
	}
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	// Validate factor.
	switch in.GetFactor() {
	case authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP,
		authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD:
	default:
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidFactor)
	}

	// Load user + totp state — both factor paths need it.
	user, err := repository.GetUserByID(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, statusError(connect.CodeUnauthenticated, StatusInvalidUserID)
		}
		return nil, internalErr(err, "pg_get_user_regenerate")
	}
	if !user.TOTPEnabled {
		return nil, statusError(connect.CodeFailedPrecondition, StatusNotEnrolled)
	}

	// Factor verification.
	if !s.verifyRegenerateFactor(ctx, in, user, now) {
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}

	// Bulk-mark current unused codes as used + insert 10 fresh codes.
	ipHash := oauthpkg.HashClientIP(in.GetClientIp())
	uaHash := oauthpkg.HashUserAgent(in.GetUserAgent())
	if err := repository.MarkAllRecoveryCodesUnused(ctx, s.DB, userID, "user_initiated", ipHash, uaHash); err != nil {
		return nil, internalErr(err, "pg_mark_all_recovery")
	}
	codes, err := recovery.GenerateSet()
	if err != nil {
		return nil, internalErr(err, "recovery_gen_regenerate")
	}
	hashes := make([]string, 0, len(codes))
	for _, c := range codes {
		h, hErr := recovery.Hash(c)
		if hErr != nil {
			return nil, internalErr(hErr, "recovery_hash_regenerate")
		}
		hashes = append(hashes, h)
	}
	if err := repository.BulkInsertRecoveryCodes(ctx, s.DB, userID, hashes); err != nil {
		return nil, internalErr(err, "pg_bulk_insert_recovery")
	}

	// Out-of-band email.
	if s.Notification != nil {
		alertErr := s.Notification.SendSecurityAlert(ctx, notification.Alert2FARecoveryRegenerated, user.Email, user.Locale, map[string]string{
			"time":       now.UTC().Format(time.RFC3339),
			"ip_summary": maskIP(in.GetClientIp()),
			"ua_summary": summarizeUA(in.GetUserAgent()),
		})
		if alertErr != nil {
			s.logWarn(ctx, "2fa_recovery_regenerated email send failed", "error", alertErr.Error())
		}
	}

	// MEDIUM-severity audit.
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FARecoveryRegenerated,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":            audit.SeverityMedium,
			"regenerated_reason":  "user_initiated",
			"factor":              factorString(in.GetFactor()),
		},
	})

	return connect.NewResponse(&authv1.RegenerateRecoveryCodesResponse{
		RecoveryCodes: codes,
	}), nil
}

// verifyRegenerateFactor returns true iff the supplied factor + value pair
// authenticates as the named user. Factor=TOTP runs KMS decrypt + RFC 6238
// validate; Factor=PASSWORD runs bcrypt.Compare against users.password_hash.
//
// Constant-time considerations: bcrypt.Compare is constant-time; TOTP
// validation iterates 3 windows even on early match. Either path produces
// uniform latency for the same factor-class.
func (s *AuthServer) verifyRegenerateFactor(ctx context.Context, in *authv1.RegenerateRecoveryCodesRequest, user *repository.User, now time.Time) bool {
	val := in.GetValue()
	switch in.GetFactor() {
	case authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP:
		if !isSixDigit(val) || user.TOTPSecretEncrypted == nil {
			return false
		}
		// Repository handed us the base64 string; need to decrypt.
		row, rErr := repository.GetTOTPSecret(ctx, s.DB, user.ID)
		if rErr != nil {
			return false
		}
		plain, dErr := s.KMS.Decrypt(ctx, row.EncryptedSecret)
		if dErr != nil {
			return false
		}
		return totp.Validate(plain, val, now, totp.DefaultWindow)
	case authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD:
		if val == "" || user.PasswordHash == nil {
			return false
		}
		return passwordpkg.Compare(user.PasswordHash, []byte(val)) == nil
	}
	return false
}

func factorString(f authv1.VerificationFactor) string {
	switch f {
	case authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP:
		return "totp"
	case authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD:
		return "password"
	default:
		return "unspecified"
	}
}

// itoaInt is the small integer-to-string helper used in security-alert
// email template variables. log.Default's fmt.Sprintf import is heavier;
// this stays allocation-tight for the hot recovery path.
func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if negative {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
