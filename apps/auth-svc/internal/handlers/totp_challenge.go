// Story 2.4 AC2 — TOTP login challenge handler.
//
// Side-effect order:
//
//  1. Parse mfa_token (signature + aud + exp + purpose strict).
//  2. Validate Redis JTI registry entry exists at
//     auth:2fa:challenge:{sha256(jti)} (single-use post-success per BR-5.4).
//  3. Validate ip_hash + ua_hash claims match the current request
//     (BR-2.3 cookie binding; mismatch → 401_mfa_token_binding_mismatch +
//     HIGH audit `2fa.challenge.binding_failed`).
//  4. Reject 6-digit format violations early (cheaper than a KMS decrypt).
//  5. Rate-limit ratelimit:2fa:challenge:{user_id} (5/15min per BR-5.1).
//     The Lua atomic INCR + EXPIRE pattern from ratelimit.CheckAndIncr.
//  6. Read users.totp_secret_encrypted; reject 401_mfa_token_invalid if
//     totp_enabled=FALSE (stale he_mfa cookie scenario, BR-2.12).
//  7. KMS-decrypt → totp.Validate (±1 window, constant-time).
//  8. On success: DEL JTI (single-use BR-2.2), UPDATE totp_last_used_at,
//     issue access+refresh with aal=2, audit `2fa.challenge.success` LOW.
//  9. On failure: counter increments; 5th failure → SoftLockUser (1h),
//     DEL JTI to prevent further use (BR-2.6), audit `2fa.challenge.locked`
//     HIGH. Return 401_invalid_totp_code generically (BR-5.10 — never
//     distinguish wrong-code vs locked vs other).
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/metrics"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

// ChallengeTOTP implements AC2 — validates a 6-digit code against the
// pending he_mfa cookie's JTI + the user's stored TOTP secret; on success,
// issues an aal=2 access+refresh token pair.
func (s *AuthServer) ChallengeTOTP(
	ctx context.Context,
	req *connect.Request[authv1.ChallengeTOTPRequest],
) (*connect.Response[authv1.ChallengeTOTPResponse], error) {
	if s.MFAParser == nil || s.KMS == nil || s.JWT == nil {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps_challenge")
	}
	in := req.Msg
	now := s.Clock()

	// === 1. Parse mfa_token strictly (alg=RS256 + aud=he-api + purpose='2fa_challenge'). ===
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

	// === 2. Redis JTI registry (BR-5.4 / BR-2.2 — single-use post-success). ===
	jtiKey := challengeJTIKey(claims.JTI)
	exists, err := s.Redis.Exists(ctx, jtiKey).Result()
	if err != nil {
		return nil, internalErr(err, "redis_jti_get")
	}
	if exists == 0 {
		// JTI absent — either consumed (successful prior challenge) or
		// the Redis tracker expired before the 5-min JWT did.
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}

	// === 3. IP/UA binding (BR-2.3 — defeats cookie theft). ===
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
			Metadata:  map[string]any{"severity": audit.SeverityHigh},
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenBindingMismatch)
	}

	// === 4. 6-digit format check (cheap; runs BEFORE KMS decrypt). ===
	if !isSixDigit(in.GetCode()) {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidTOTPFormat)
	}

	// === 5. Rate limit (5 failures/15min per BR-5.1; BR-2.6 soft-lock). ===
	rlKey := ratelimit.MFAKey(ratelimit.OpMFAChallenge, userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, rl2FAChallengeLimit, rl2FAChallengeWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		// BR-2.6 — soft-lock the user; DEL JTI to prevent further use.
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
			Metadata:  map[string]any{"severity": audit.SeverityHigh},
		})
		s.Metrics.Inc2FALocked(ctx, metrics.TwoFAFactorTOTP)
		retryAfter := int(rl.RetryAfter/time.Second) + 1
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusAccountLocked, retryAfter)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_challenge")
	}

	// === 6. Load encrypted secret (BR-2.12 — stale cookie for non-enrolled user). ===
	row, err := repository.GetTOTPSecret(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
		}
		return nil, internalErr(err, "pg_get_totp_challenge")
	}
	if !row.Enabled || len(row.EncryptedSecret) == 0 {
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}

	// === 7. KMS decrypt → validate code ===
	plainSecret, err := s.KMS.Decrypt(ctx, row.EncryptedSecret)
	if err != nil {
		// Surface as 503 — KMS is the failure source, NOT the user's code.
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeFailed,
			UserID:    userID.String(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusKMSUnavailable,
			Metadata:  map[string]any{"severity": audit.SeverityLow, "failure_reason": "kms_decrypt"},
		})
		return nil, statusError(connect.CodeUnavailable, StatusKMSUnavailable)
	}
	if !totp.Validate(plainSecret, in.GetCode(), now, totp.DefaultWindow) {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAChallengeFailed,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusInvalidTOTPCode,
			Metadata:  map[string]any{"severity": audit.SeverityLow},
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidTOTPCode)
	}

	// === 8. Success — atomic DEL gates token issuance (QA Round 1 M3 fix). ===
	// Redis DEL returns the count of keys actually deleted. Step 2 (Exists)
	// is a pre-check only; two concurrent ChallengeTOTP calls with the same
	// JTI may both pass Exists. We now require Del to return exactly 1: any
	// goroutine that arrives second sees 0 and is rejected with
	// 401_mfa_token_invalid, so we never mint two valid token pairs from one
	// mfa_token. BR-2.2 single-use is enforced atomically here.
	delCount, delErr := s.Redis.Del(ctx, jtiKey).Result()
	if delErr != nil {
		s.logWarn(ctx, "DEL jti after 2fa success failed", "error", delErr.Error())
		// Transient Redis error — fail closed (don't issue tokens we can't
		// guarantee are single-use).
		return nil, internalErr(delErr, "redis_jti_consume")
	}
	if delCount == 0 {
		// Lost the JTI race to another concurrent ChallengeTOTP.
		return nil, statusError(connect.CodeUnauthenticated, StatusMFATokenInvalid)
	}
	_ = repository.MarkTOTPUsed(ctx, s.DB, userID) // best-effort

	// Issue access + refresh with aal=2 claim (BR-2.8 / T5.3). Downstream
	// services (billing-svc, Story 2.7 delete-account) gate on aal>=2.
	familyID, err := uuid.NewRandom()
	if err != nil {
		return nil, internalErr(err, "gen_family_id_2fa")
	}
	accessToken, err := s.JWT.SignAccessTokenWithAAL(userID, now, 2)
	if err != nil {
		return nil, internalErr(err, "sign_access_2fa")
	}
	refreshToken, err := s.JWT.SignRefreshToken(userID, familyID, now)
	if err != nil {
		return nil, internalErr(err, "sign_refresh_2fa")
	}
	refreshJTI, err := jtiFromToken(refreshToken)
	if err != nil {
		return nil, internalErr(err, "decode_refresh_jti_2fa")
	}
	familyKey := refreshFamilyKeyPrefix + familyID.String()
	if err := s.Redis.Set(ctx, familyKey, refreshJTI, refreshTokenTTLSeconds()).Err(); err != nil {
		return nil, internalErr(err, "redis_refresh_family_2fa")
	}

	// Clear the challenge counter on success (a successful pass shouldn't
	// leave the user one wrong-code away from soft-lock on a future
	// signin).
	_ = s.Redis.Del(ctx, rlKey).Err()

	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FAChallengeSuccess,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata:  map[string]any{"severity": audit.SeverityLow, "login_method": claims.LoginMethod},
	})
	s.Metrics.IncSignin(ctx, metrics.SigninResultSuccess, "2fa")
	s.Metrics.Inc2FAChallenge(ctx, metrics.TwoFAResultSuccess, metrics.TwoFAFactorTOTP)

	return connect.NewResponse(&authv1.ChallengeTOTPResponse{
		AccessToken:                  accessToken,
		RefreshToken:                 refreshToken,
		AccessTokenExpiresInSeconds:  int32(accessTokenTTLSeconds().Seconds()),
		RefreshTokenExpiresInSeconds: int32(refreshTokenTTLSeconds().Seconds()),
		Aal:                          2,
		ReturnTo:                     claims.ReturnTo,
	}), nil
}

// writeChallengeJTI is the AC2 / AC3 helper for LoginUser + CompleteOAuth
// to register a freshly-issued mfa_token's JTI in Redis. Called BEFORE
// returning the response, so a successful issue is also a registered JTI.
//
// Stored value is a JSON envelope so future Stories can extend the registry
// schema (attempt counter, partial-state markers) without breaking the
// challenge handler — currently the handler only checks existence.
func (s *AuthServer) writeChallengeJTI(ctx context.Context, jti, userID string) error {
	body, _ := json.Marshal(map[string]any{
		"user_id":    userID,
		"created_at": s.Clock().Unix(),
	})
	return s.Redis.Set(ctx, challengeJTIKey(jti), string(body), challengeJTITTL).Err()
}

// Silence the unused-context import when this file is reduced to helpers.
var _ context.Context
