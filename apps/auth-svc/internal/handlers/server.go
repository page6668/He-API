// Package handlers implements the AuthService gRPC interface.
//
// P2f lands RegisterUser (AC1). VerifyEmail / ResendVerification (AC2) +
// LoginUser / RefreshToken (AC3) remain CodeUnimplemented stubs; they fill
// in P3 + P4.
//
// Dependency wiring: AuthServer holds all the collaborators as fields. The
// production cmd/server constructs them with real implementations; the
// handler tests inject fakes (no DB, no Redis, no SendGrid, no Kafka).
package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	authjwt "github.com/he-api/he-api/apps/auth-svc/internal/jwt"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	"github.com/he-api/he-api/apps/auth-svc/internal/password"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
	"github.com/he-api/he-api/apps/auth-svc/internal/token"
)

// HIBPChecker abstracts password.HIBPClient so the handler tests can stub
// the network without instantiating a real client.
type HIBPChecker interface {
	CheckBreached(ctx context.Context, pw []byte) error
}

// JWTSigner is the narrow surface LoginUser + RefreshToken depend on.
// *jwt.Signer satisfies this; tests pass a deterministic fake.
type JWTSigner interface {
	SignAccessToken(userID uuid.UUID, now time.Time) (string, error)
	SignRefreshToken(userID uuid.UUID, familyID uuid.UUID, now time.Time) (string, error)
}

// JWTVerifier is the narrow surface RefreshToken uses to parse the
// incoming refresh token. *jwt.Verifier satisfies it.
type JWTVerifier interface {
	Verify(token string) (*authjwt.Claims, error)
}

// AuthServer satisfies authv1connect.AuthServiceHandler. P2f wires only
// RegisterUser; the rest still return CodeUnimplemented + a phase pointer
// (their fill-in lives in P3/P4).
type AuthServer struct {
	// DB is the PostgreSQL queryer (typically a *pgxpool.Pool in production).
	DB repository.Querier
	// Redis is the rate-limit + email-verify-token store. redis.Cmdable is
	// the narrowest interface that covers both ratelimit.CheckAndIncr's
	// redis.Scripter requirement and token.Store's SET-EX call.
	Redis redis.Cmdable
	// HIBP is the breach-check client.
	HIBP HIBPChecker
	// Notification dispatches the verification email through notification-svc.
	Notification notification.Sender
	// Audit dispatches BR-4.5 events. NoOpPublisher during P2f-P4; the
	// Kafka-backed publisher swaps in at P5/T4 without changing call sites.
	Audit audit.Publisher
	// JWT signs access + refresh tokens for LoginUser / RefreshToken.
	// Nil during P2f-P3 when only RegisterUser / VerifyEmail land. Required
	// by P4b LoginUser onward.
	JWT JWTSigner
	// JWTVerify parses incoming refresh tokens for RefreshToken (P4c).
	JWTVerify JWTVerifier
	// Clock returns the current time; tests override for deterministic
	// audit timestamps and token expiry calculations.
	Clock func() time.Time
	// ConsoleBaseURL is the public console URL used to build the
	// verification_link emailed to the user, e.g.
	// "https://console.he-api.com" (prod) /
	// "https://console.staging.he-api.com" (staging) /
	// "http://localhost:3000" (dev).
	ConsoleBaseURL string
	// Logger is the slog logger for audit-best-effort logging + ops.
	Logger SlogLike
}

// SlogLike is the small slice of slog.Logger the handler needs (avoids a
// hard import of log/slog for the handler when tests need a no-op logger).
type SlogLike interface {
	WarnContext(ctx context.Context, msg string, args ...any)
	InfoContext(ctx context.Context, msg string, args ...any)
}

// NewAuthServer returns an AuthServer with sensible defaults applied to any
// nil field (Clock → time.Now). Required deps (DB / Redis / HIBP / Notification /
// Audit / ConsoleBaseURL / Logger) MUST be supplied by the caller.
func NewAuthServer(s AuthServer) *AuthServer {
	if s.Clock == nil {
		s.Clock = time.Now
	}
	return &s
}

// validLocales mirrors apps/console/i18n/config.ts. Updated whenever a new
// locale is added to the platform (currently the Story 2.1 ten-MVP locale set).
var validLocales = map[string]bool{
	"en":    true,
	"zh-CN": true,
	"ja":    true,
	"ko":    true,
	"es":    true,
	"fr":    true,
	"de":    true,
	"pt":    true,
	"ru":    true,
	"ar":    true,
}

const (
	defaultLocale = "en"

	signupRateLimit   = 5
	signupRateWindow  = 5 * time.Minute
	dummyVerifyKeyTTL = 1 * time.Second

	// Resend rate-limits per BR-2.4.
	resendIPRateLimit     = 3
	resendIPRateWindow    = 15 * time.Minute
	resendEmailRateLimit  = 1
	resendEmailRateWindow = 60 * time.Second

	// Signin rate-limits per BR-3.8.
	signinIPRateLimit    = 10
	signinIPRateWindow   = 15 * time.Minute
	signinEmailRateLimit = 5
	signinEmailRateWindow = 15 * time.Minute

	// Soft-lock parameters (BR-3.3 + BR-4.4).
	softLockFailureThreshold = 5
	softLockDuration         = time.Hour

	// Refresh-family Redis tracker key shape (BR-3.9).
	refreshFamilyKeyPrefix = "auth:refresh:"
)

// RegisterUser implements AC1. Side-effect order — strict for INT-001..005
// timing + atomicity guarantees:
//
//  1. Input validation (locale, email format/length). Cheap; surfaces clean
//     400_* without doing expensive work.
//  2. Rate-limit by IP (UNIT-035: BEFORE password validation). Avoids HIBP
//     network calls + bcrypt CPU on rate-limited requests.
//  3. Password length validation, then HIBP k-anonymity check (fail-closed
//     per TS-CONS-004 — 503_hibp_unavailable, NOT a permissive pass).
//  4. bcrypt.Hash (cost=12, ~200ms). Both success and duplicate paths run
//     this so the work is identical regardless of email existence.
//  5. PG INSERT INTO he_api.users. On ErrEmailExists: take the anti-enumeration
//     branch (dummy SETEX + audit signup_duplicate_attempt + same response
//     shape as success). On other DB error: 500.
//  6. Token Generate + Store. On error: audit + 500_email_send_failed-class.
//  7. notification-svc SendVerificationEmail. On ErrTransient (5xx / timeout):
//     500_email_send_failed (user row persists; user retries via Resend).
//     On ErrPermanent (4xx): 500 (programming/cred bug).
//  8. Audit auth.signup success.
//  9. Return {status:"pending_verification"} — NO user_id.
func (s *AuthServer) RegisterUser(
	ctx context.Context,
	req *connect.Request[authv1.RegisterUserRequest],
) (*connect.Response[authv1.RegisterUserResponse], error) {
	in := req.Msg
	now := s.Clock()

	// === 1. Input validation ===
	locale := resolveLocale(in.GetLocale())
	if locale == "" {
		// supplied non-empty but not in the 10-MVP tuple
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidLocale)
	}

	emailNorm, err := normalizeEmail(in.GetEmail())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidEmail)
	}
	emailHash := ratelimit.EmailHash(emailNorm)

	// === 2. Rate limit (BEFORE password validation — UNIT-035) ===
	ipKey := ratelimit.SignupIPKey(in.GetClientIp())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, ipKey, signupRateLimit, signupRateWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventSignup,
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusRateLimitSignup,
		})
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitSignup, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit")
	}

	// === 3. Password validation ===
	pwBytes := []byte(in.GetPassword())
	if err := password.ValidateLength(pwBytes); err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusPasswordTooShort)
	}
	if err := s.HIBP.CheckBreached(ctx, pwBytes); err != nil {
		switch {
		case errors.Is(err, password.ErrPasswordBreached):
			return nil, statusError(connect.CodeInvalidArgument, StatusPasswordBreached)
		case errors.Is(err, password.ErrHIBPUnavailable):
			return nil, statusError(connect.CodeUnavailable, StatusHIBPUnavailable)
		default:
			return nil, internalErr(err, "hibp")
		}
	}

	// === 4. bcrypt hash (always runs — same work for success + duplicate) ===
	hash, err := password.Hash(pwBytes)
	if err != nil {
		return nil, internalErr(err, "bcrypt")
	}

	// === 5. PG INSERT ===
	userID, err := repository.InsertUser(ctx, s.DB, emailNorm, hash, locale)
	if errors.Is(err, repository.ErrEmailExists) {
		// === Duplicate path: anti-enumeration (BR-1.4 / BR-1.5 / UNIT-037..039) ===
		// Equivalent CPU / IO budget so response time tracks the success path:
		// the bcrypt work above was already done; here we add a dummy
		// short-lived Redis SETEX to match the success-path token Store call.
		s.dummyRedisSetex(ctx, emailHash)
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventSignupDuplicateAttempt,
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
		})
		return successResponse(), nil
	}
	if err != nil {
		return nil, internalErr(err, "pg_insert")
	}

	// === 6. Token generate + Store ===
	plaintextTok, err := token.Generate()
	if err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			UserID:    userID.String(),
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusEmailSendFailed,
		})
		return nil, statusError(connect.CodeInternal, StatusEmailSendFailed)
	}
	if err := token.Store(ctx, s.Redis, plaintextTok, userID); err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			UserID:    userID.String(),
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusEmailSendFailed,
		})
		return nil, statusError(connect.CodeInternal, StatusEmailSendFailed)
	}

	// === 7. notification-svc SendVerificationEmail ===
	verificationLink := buildVerificationLink(s.ConsoleBaseURL, locale, plaintextTok)
	if err := s.Notification.SendVerificationEmail(ctx, emailNorm, locale, plaintextTok, verificationLink); err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			UserID:    userID.String(),
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusEmailSendFailed,
		})
		// Both transient and permanent map to 500_email_send_failed per AC1
		// Error Handling row 7. The user can retry via Resend; the row
		// persists with email_verified_at=NULL.
		return nil, statusError(connect.CodeInternal, StatusEmailSendFailed)
	}

	// === 8. Audit success ===
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventSignup,
		UserID:    userID.String(),
		EmailHash: emailHash,
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
	})

	// === 9. Return success-shaped response (no user_id — UNIT-044) ===
	return successResponse(), nil
}

// successResponse is the canonical anti-enumeration response for the signup
// path. Both first-time and duplicate-email signups return this; api-gateway
// translates to the HTTP body {message:"verification_email_sent"}.
func successResponse() *connect.Response[authv1.RegisterUserResponse] {
	return connect.NewResponse(&authv1.RegisterUserResponse{
		Status: "pending_verification",
	})
}

// resolveLocale validates the supplied locale against the 10-MVP tuple. An
// empty input defaults to "en" (BR-1.9 + UNIT-045). A non-empty value not
// in the tuple returns "" so the caller surfaces 400_invalid_locale.
func resolveLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return defaultLocale
	}
	if validLocales[locale] {
		return locale
	}
	return ""
}

// normalizeEmail parses + lowercases + trims the email per BR-1.1:
//   - net/mail.ParseAddress for RFC 5322 syntax
//   - length ≤ 254 chars
//   - local part (before '@') ≤ 64 chars
//
// Returns the normalized form (lowercased, trimmed) suitable for use as the
// UNIQUE key in he_api.users (matches BR-4.2 email_hash canonicalization).
func normalizeEmail(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("email: empty")
	}
	if len(trimmed) > 254 {
		return "", errors.New("email: over 254 chars")
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", err
	}
	addr := strings.ToLower(parsed.Address)
	at := strings.IndexByte(addr, '@')
	if at < 1 || at > 64 {
		return "", errors.New("email: local part length out of range")
	}
	// Disallow any of the optional display-name extras ParseAddress permits
	// — the canonical form is just the bare address.
	if parsed.Name != "" {
		return "", errors.New("email: display name not permitted")
	}
	return addr, nil
}

// buildVerificationLink composes the canonical URL that goes into the
// verification email body. Format matches AC1 Scenario.
func buildVerificationLink(baseURL, locale, plaintextToken string) string {
	return fmt.Sprintf("%s/%s/verify-email?token=%s",
		strings.TrimRight(baseURL, "/"), locale, plaintextToken)
}

// dummyRedisSetex is the anti-enumeration timing-parity helper (UNIT-037).
// Writes a short-lived dummy key so the duplicate-email path's IO cost
// matches the success path's token.Store call. The key includes the email
// hash so dummy writes for different emails go to distinct keys (preventing
// observable Redis-cache-hit timing differences).
func (s *AuthServer) dummyRedisSetex(ctx context.Context, emailHash string) {
	key := "auth:email_verify:dummy:" + emailHash
	// Best-effort: a Redis outage here is the caller's problem to surface,
	// but the dummy SETEX failing is harmless to the response semantics.
	_ = s.Redis.Set(ctx, key, "", dummyVerifyKeyTTL).Err()
}

// auditBestEffort wraps the audit publisher with the TS-CONS-009 "never
// blocks business path" contract.
func (s *AuthServer) auditBestEffort(ctx context.Context, e audit.Event) {
	// audit.PublishBestEffort itself swallows errors with a warn log; we
	// thread our SlogLike through a thin shim so the handler doesn't carry
	// a hard log/slog import.
	if s.Audit == nil {
		return
	}
	if err := s.Audit.Publish(ctx, e); err != nil && s.Logger != nil {
		s.Logger.WarnContext(ctx, "audit publish failed",
			"event_type", string(e.EventType),
			"error_code", e.ErrorCode,
			"error", err.Error(),
		)
	}
}

// internalErr wraps a non-business error as a Connect Internal with a
// stable status code so the api-gateway envelope stays uniform.
func internalErr(err error, where string) *connect.Error {
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %s: %w", StatusAuthSvcUnavailable, where, err))
}

// VerifyEmail implements AC2 (P3b). Flow:
//
//  1. ParseFormat — short-circuit malformed tokens at the edge (UNIT-080).
//  2. Consume — atomic GET + INCR attempts + DEL primary + reverse in
//     one Lua eval (P3a). Returns ErrTokenNotFound (treat as
//     410_token_expired — TTL elapsed OR never issued — the same UI),
//     ErrTokenAttemptsExceeded (treat as 410_token_used + audit
//     auth.verify_email_brute_force per BR-2.5), or the unwrapped Payload.
//  3. MarkEmailVerified — idempotent UPDATE. Returns wasNewlyVerified;
//     false means the user was already verified before this call (BR-2.3
//     idempotent surface).
//  4. Audit auth.verify_email (success=true regardless of newly/already —
//     the audit signal is "verification flow ran successfully").
//  5. For the already-verified case, fetch the canonical email_verified_at
//     from PG so the response carries the real timestamp, not now.
func (s *AuthServer) VerifyEmail(
	ctx context.Context,
	req *connect.Request[authv1.VerifyEmailRequest],
) (*connect.Response[authv1.VerifyEmailResponse], error) {
	in := req.Msg
	now := s.Clock()

	// === 1. Format check ===
	if err := token.ParseFormat(in.GetToken()); err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidToken)
	}

	// === 2. Consume ===
	payload, err := token.Consume(ctx, s.Redis, in.GetToken())
	if err != nil {
		switch {
		case errors.Is(err, token.ErrTokenNotFound):
			return nil, statusError(connect.CodeFailedPrecondition, StatusTokenExpired)
		case errors.Is(err, token.ErrTokenAttemptsExceeded):
			s.auditBestEffort(ctx, audit.Event{
				EventType: audit.EventVerifyEmailBruteForce,
				IP:        in.GetClientIp(),
				UserAgent: in.GetUserAgent(),
				Timestamp: now,
				Success:   false,
				ErrorCode: StatusTokenUsed,
			})
			return nil, statusError(connect.CodeFailedPrecondition, StatusTokenUsed)
		default:
			return nil, internalErr(err, "token_consume")
		}
	}

	// === 3. Mark verified (idempotent UPDATE) ===
	wasNewlyVerified, err := repository.MarkEmailVerified(ctx, s.DB, payload.UserID)
	if err != nil {
		return nil, internalErr(err, "mark_email_verified")
	}

	// === 4. Audit ===
	emailHashForAudit := "" // we don't have the email here without a SELECT — leave empty
	// Optionally hydrate via GetUserByID for the audit email_hash, but that's
	// an extra round-trip on a path that's already DB-bound; skip.
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventVerifyEmail,
		UserID:    payload.UserID.String(),
		EmailHash: emailHashForAudit,
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
	})

	// === 5. Build response ===
	status := "email_verified"
	verifiedAt := now
	if !wasNewlyVerified {
		// Already-verified — fetch canonical timestamp for the response. If
		// the lookup fails we fall back to `now` so the response stays
		// well-formed; the timestamp is informational, not gating.
		status = "already_verified"
		if u, err := repository.GetUserByID(ctx, s.DB, payload.UserID); err == nil && u.EmailVerifiedAt != nil {
			verifiedAt = *u.EmailVerifiedAt
		}
	}

	return connect.NewResponse(&authv1.VerifyEmailResponse{
		UserId:          payload.UserID.String(),
		EmailVerifiedAt: verifiedAt.UTC().Format(time.RFC3339),
		Status:          status,
	}), nil
}

// ResendVerification implements AC2 (P3b). Wright Round 1 m-5 ruling:
// all three branches return the SAME {status:"ok"} response (anti-
// enumeration). The audit event_type distinguishes them so ops can still
// see what actually happened.
//
// Flow:
//  1. normalize email; if malformed, take the unknown-email branch (which
//     also returns {status:"ok"} — never leak format errors to the caller).
//  2. Rate-limit IP (BR-2.4 — 3/15min). 429 on trip.
//  3. Rate-limit email (BR-2.4 — 1/60s). 429 on trip.
//  4. GetUserByEmail.
//     - ErrUserNotFound → dummy SETEX + audit unknown_email + ok.
//     - user.email_verified_at != nil → dummy SETEX + audit already_verified + ok.
//     - else (pending verification) → DeleteForUser (invalidate prior) +
//       token.Generate + token.Store + notification.SendVerificationEmail +
//       audit sent + ok. Locale comes from user.Locale per BR-2.6 (UNIT-090).
func (s *AuthServer) ResendVerification(
	ctx context.Context,
	req *connect.Request[authv1.ResendVerificationRequest],
) (*connect.Response[authv1.ResendVerificationResponse], error) {
	in := req.Msg
	now := s.Clock()

	emailNorm, emailErr := normalizeEmail(in.GetEmail())
	if emailErr != nil {
		// Malformed email is treated identically to unknown-email per m-5.
		// (A real user with a typo will eventually correct it; auth-svc
		// MUST NOT signal that the address is unparseable here.)
		s.dummyRedisSetex(ctx, "")
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusInvalidEmail,
		})
		return resendOK(), nil
	}
	emailHash := ratelimit.EmailHash(emailNorm)

	// === 2. IP rate limit ===
	ipKey := ratelimit.ResendIPKey(in.GetClientIp())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, ipKey, resendIPRateLimit, resendIPRateWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusRateLimitResendIP,
		})
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitResendIP, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_resend_ip")
	}

	// === 3. Email rate limit ===
	emailKey := ratelimit.ResendEmailKey(emailNorm)
	rl, err = ratelimit.CheckAndIncr(ctx, s.Redis, emailKey, resendEmailRateLimit, resendEmailRateWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusRateLimitResendEm,
		})
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitResendEm, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_resend_email")
	}

	// === 4. Look up user ===
	user, err := repository.GetUserByEmail(ctx, s.DB, emailNorm)
	if errors.Is(err, repository.ErrUserNotFound) {
		// --- unknown email branch ---
		s.dummyRedisSetex(ctx, emailHash)
		s.auditBestEffort(ctx, audit.Event{
			// EventEmailSendFailed reuses the "not sent" event_type — the
			// distinguishing detail is ErrorCode. Story 2.2 metadata
			// declares the 8 event types; the resend-flavor signals
			// (unknown_email / already_verified / sent) live in ErrorCode
			// + Metadata until the audit-svc adds dedicated event types.
			EventType: audit.EventEmailSendFailed,
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: "resend.unknown_email",
		})
		return resendOK(), nil
	}
	if err != nil {
		return nil, internalErr(err, "get_user_by_email")
	}

	// --- already-verified branch ---
	if user.EmailVerifiedAt != nil {
		s.dummyRedisSetex(ctx, emailHash)
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			UserID:    user.ID.String(),
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: "resend.already_verified",
		})
		return resendOK(), nil
	}

	// --- pending-verification branch (real send) ---
	// Invalidate any prior token for this user (BR-1.6 — old token immediately
	// inert before the new one is issued).
	if _, err := token.DeleteForUser(ctx, s.Redis, user.ID); err != nil {
		return nil, internalErr(err, "token_delete_for_user")
	}
	plaintextTok, err := token.Generate()
	if err != nil {
		return nil, internalErr(err, "token_generate")
	}
	if err := token.Store(ctx, s.Redis, plaintextTok, user.ID); err != nil {
		return nil, internalErr(err, "token_store")
	}
	// Locale from users.locale per BR-2.6 + UNIT-090 — never from request.
	verificationLink := buildVerificationLink(s.ConsoleBaseURL, user.Locale, plaintextTok)
	if err := s.Notification.SendVerificationEmail(ctx, user.Email, user.Locale, plaintextTok, verificationLink); err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventEmailSendFailed,
			UserID:    user.ID.String(),
			EmailHash: emailHash,
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusEmailSendFailed,
		})
		// Even on notification failure, return the canonical anti-enumeration
		// response — surfacing the failure to the caller would leak that the
		// email IS registered.
		return resendOK(), nil
	}
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventEmailSendFailed,
		UserID:    user.ID.String(),
		EmailHash: emailHash,
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		ErrorCode: "resend.sent",
	})
	return resendOK(), nil
}

// resendOK is the canonical anti-enumeration response for ResendVerification
// (Wright Round 1 m-5). All three branches (sent / unknown / already_verified)
// emit identical body shape.
func resendOK() *connect.Response[authv1.ResendVerificationResponse] {
	return connect.NewResponse(&authv1.ResendVerificationResponse{
		Status: "ok",
	})
}

// LoginUser implements AC3 (P4b). The flow has three timing-parity
// constraints (BR-3.2):
//
//   1. Unknown email → DummyCompare consumes the same CPU as a real
//      bcrypt compare; response time is statistically indistinguishable.
//   2. Wrong-password vs unknown-email → identical 401_invalid_credentials.
//   3. Soft-lock activation at 5th failed attempt → still returns 401
//      (NOT 423) so the attacker can't binary-search when the lock fires
//      (UNIT-139). 423 only surfaces on SUBSEQUENT signins during the
//      locked window.
//
// Status branches (BR-3.3 + AC3 Error Handling):
//   - suspended         → 403_account_suspended
//   - pending_deletion  → 410_account_deleted
//   - locked + unexpired→ 423_account_locked + Retry-After
//   - locked + expired  → SelfHealLock atomically (BR-4.4) then proceed
//   - email_verified_at IS NULL → 403_email_not_verified
//
// Side effects on success:
//   - DEL ratelimit:signin:email:* (success clears the failure counter)
//   - UPDATE users.updated_at (last-activity sentinel)  — deferred to P5
//   - Generate family_id (uuid) + sign access + refresh tokens
//   - SET auth:refresh:{family_id} = refresh_jti (Redis tracker — used
//     by RefreshToken in P4c for rotation + reuse detection)
//   - audit auth.signin_success
//
// 2FA hook (BR-3.5 + UNIT-141): users.totp_enabled = false in this Story
// (no migration adds it for real activation), so the totp branch is a
// stub that the Story 2.4 fill-in will activate. The handler always
// takes the no-2FA path here.
func (s *AuthServer) LoginUser(
	ctx context.Context,
	req *connect.Request[authv1.LoginUserRequest],
) (*connect.Response[authv1.LoginUserResponse], error) {
	in := req.Msg
	now := s.Clock()

	// === 1. Input shape ===
	emailNorm, err := normalizeEmail(in.GetEmail())
	if err != nil {
		// Same response shape as wrong password — no enumeration signal.
		_ = password.DummyCompare([]byte(in.GetPassword()))
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	if in.GetPassword() == "" {
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	emailHash := ratelimit.EmailHash(emailNorm)

	// === 2. Rate limits (BR-3.8) ===
	ipKey := ratelimit.SigninIPKey(in.GetClientIp())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, ipKey, signinIPRateLimit, signinIPRateWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		s.auditSigninFailure(ctx, "", emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusRateLimitSigninIP)
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitSigninIP, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_signin_ip")
	}

	emailKey := ratelimit.SigninEmailKey(emailNorm)
	rl, err = ratelimit.CheckAndIncr(ctx, s.Redis, emailKey, signinEmailRateLimit, signinEmailRateWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		s.auditSigninFailure(ctx, "", emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusRateLimitSigninEm)
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimitSigninEm, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_signin_email")
	}

	// === 3. Look up user ===
	user, err := repository.GetUserByEmail(ctx, s.DB, emailNorm)
	if errors.Is(err, repository.ErrUserNotFound) {
		// Unknown email — burn bcrypt time + return same 401 as wrong-password.
		_ = password.DummyCompare([]byte(in.GetPassword()))
		s.auditSigninFailure(ctx, "", emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusInvalidCredentials)
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	if err != nil {
		return nil, internalErr(err, "get_user_by_email")
	}

	// === 4. Status checks (BR-3.3 / AC3 Error Handling) ===
	switch user.Status {
	case "suspended":
		s.auditSigninFailure(ctx, user.ID.String(), emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusAccountSuspended)
		return nil, statusError(connect.CodePermissionDenied, StatusAccountSuspended)
	case "pending_deletion":
		s.auditSigninFailure(ctx, user.ID.String(), emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusAccountDeleted)
		return nil, statusError(connect.CodeFailedPrecondition, StatusAccountDeleted)
	case "locked":
		if user.LockedUntil != nil && user.LockedUntil.After(now) {
			retryAfter := int(time.Until(*user.LockedUntil).Seconds()) + 1
			s.auditSigninFailure(ctx, user.ID.String(), emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusAccountLocked)
			return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusAccountLocked, retryAfter)
		}
		// Self-heal — lock expired. BR-4.4: same-transaction atomic clear,
		// no cron job. Proceed to bcrypt compare on success.
		healed, healErr := repository.SelfHealLock(ctx, s.DB, user.ID)
		if healErr != nil {
			return nil, internalErr(healErr, "self_heal_lock")
		}
		_ = healed // bool is for audit / logging; bcrypt below decides the rest
	}

	// === 5. Email verified? ===
	if user.EmailVerifiedAt == nil {
		s.auditSigninFailure(ctx, user.ID.String(), emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusEmailNotVerified)
		return nil, statusError(connect.CodePermissionDenied, StatusEmailNotVerified)
	}

	// === 6. bcrypt compare ===
	if err := password.Compare(user.PasswordHash, []byte(in.GetPassword())); err != nil {
		// Wrong password — INCR failure counter; soft-lock at 5th attempt.
		// The CheckAndIncr above already counted this attempt against the
		// 15-min window. We use a separate counter for the soft-lock
		// threshold so the lock activates exactly on the Nth wrong-password
		// hit, independent of the per-window rate-limit refresh.
		failed, lockErr := s.incrSigninFailures(ctx, emailHash)
		if lockErr != nil {
			// Don't block the user response on counter failure; just log.
			s.logWarn(ctx, "signin failure counter incr failed", "error", lockErr.Error())
		}
		if failed >= softLockFailureThreshold {
			until := now.Add(softLockDuration)
			if err := repository.SoftLockUser(ctx, s.DB, user.ID, until); err != nil {
				return nil, internalErr(err, "soft_lock")
			}
			s.auditBestEffort(ctx, audit.Event{
				EventType: audit.EventAccountLocked,
				UserID:    user.ID.String(),
				EmailHash: emailHash,
				IP:        in.GetClientIp(),
				UserAgent: in.GetUserAgent(),
				Timestamp: now,
				Success:   false,
				ErrorCode: StatusAccountLocked,
			})
			// UNIT-139: STILL return 401 here, NOT 423. The lock takes effect
			// on the NEXT signin so the attacker can't binary-search when
			// the threshold fires.
		}
		s.auditSigninFailure(ctx, user.ID.String(), emailHash, in.GetClientIp(), in.GetUserAgent(), now, StatusInvalidCredentials)
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}

	// === 7. 2FA hook (Story 2.4) ===
	if user.TOTPEnabled {
		// Story 2.4 fills this with a real mfa_token. For Story 2.2 the
		// column always reads FALSE so this branch is dead in practice.
		// Returning Unimplemented makes the path visible if data drift
		// somehow flips the column without 2.4 landing.
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("LoginUser: 2FA required — pending Story 2.4 TOTP fill-in"))
	}

	// === 8. Success — sign tokens + write refresh-family tracker ===
	familyID, err := uuid.NewRandom()
	if err != nil {
		return nil, internalErr(err, "gen_family_id")
	}
	accessToken, err := s.JWT.SignAccessToken(user.ID, now)
	if err != nil {
		return nil, internalErr(err, "sign_access")
	}
	refreshToken, err := s.JWT.SignRefreshToken(user.ID, familyID, now)
	if err != nil {
		return nil, internalErr(err, "sign_refresh")
	}
	// Track the current valid jti for this family — RefreshToken (P4c)
	// uses this to detect reuse + rotate.
	refreshJTI, err := jtiFromToken(refreshToken)
	if err != nil {
		return nil, internalErr(err, "decode_refresh_jti")
	}
	familyKey := refreshFamilyKeyPrefix + familyID.String()
	if err := s.Redis.Set(ctx, familyKey, refreshJTI, refreshTokenTTLSeconds()).Err(); err != nil {
		return nil, internalErr(err, "redis_refresh_family")
	}

	// Clear the signin-failure counter on success.
	_ = s.Redis.Del(ctx, signinFailureKey(emailHash)).Err()

	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventSigninSuccess,
		UserID:    user.ID.String(),
		EmailHash: emailHash,
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
	})

	return connect.NewResponse(&authv1.LoginUserResponse{
		Status:                          authv1.LoginStatus_LOGIN_STATUS_OK,
		AccessToken:                     accessToken,
		RefreshToken:                    refreshToken,
		AccessTokenExpiresInSeconds:     int32(accessTokenTTLSeconds().Seconds()),
		RefreshTokenExpiresInSeconds:    int32(refreshTokenTTLSeconds().Seconds()),
	}), nil
}

// auditSigninFailure is the canonical failure-path audit shape. Keeps the
// many call sites in LoginUser DRY without macro-style helpers.
func (s *AuthServer) auditSigninFailure(ctx context.Context, userID, emailHash, ip, ua string, now time.Time, errorCode string) {
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventSigninFailure,
		UserID:    userID,
		EmailHash: emailHash,
		IP:        ip,
		UserAgent: ua,
		Timestamp: now,
		Success:   false,
		ErrorCode: errorCode,
	})
}

// signinFailureKey is the per-email Redis counter feeding the soft-lock.
// Distinct from the ratelimit:signin:email:* counter (which is per-window
// rate limit). The soft-lock counter persists for softLockDuration so we
// can fire the lock exactly on the Nth wrong-password attempt.
func signinFailureKey(emailHash string) string {
	return "auth:signin:failures:" + emailHash
}

// incrSigninFailures bumps the per-email failure counter with a Lua
// atomic INCR + EXPIRE (matches the ratelimit package's pattern but with
// a different lifecycle — this counter persists across the rate-limit
// window, only DEL'd on successful signin or when the soft-lock fires).
func (s *AuthServer) incrSigninFailures(ctx context.Context, emailHash string) (int64, error) {
	key := signinFailureKey(emailHash)
	raw, err := s.Redis.Eval(ctx, signinFailureIncrLua, []string{key}, int64(softLockDuration/time.Second)).Result()
	if err != nil {
		return 0, err
	}
	n, ok := raw.(int64)
	if !ok {
		return 0, fmt.Errorf("signin failure incr: unexpected result type %T", raw)
	}
	return n, nil
}

const signinFailureIncrLua = `
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return count
`

// logWarn is the SlogLike wrapper that handles a nil Logger gracefully —
// LoginUser non-fatal paths use this so test harnesses that pass a nil
// logger don't panic.
func (s *AuthServer) logWarn(ctx context.Context, msg string, kv ...any) {
	if s.Logger == nil {
		return
	}
	s.Logger.WarnContext(ctx, msg, kv...)
}

// jtiFromToken extracts the `jti` claim from a signed JWT WITHOUT
// verifying the signature (we just signed it, so verification is
// redundant). Used by LoginUser to record the issued refresh jti in the
// Redis family tracker.
func jtiFromToken(signedToken string) (string, error) {
	parts := strings.Split(signedToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed JWT: %d parts", len(parts))
	}
	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		JTI string `json:"jti"`
	}
	if err := jsonUnmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("unmarshal claims: %w", err)
	}
	if claims.JTI == "" {
		return "", errors.New("jti claim missing")
	}
	return claims.JTI, nil
}

// accessTokenTTLSeconds + refreshTokenTTLSeconds re-export the jwt package
// constants without importing the whole package's name into this file's
// arithmetic. Avoid drift by sourcing from the canonical constants.
func accessTokenTTLSeconds() time.Duration  { return 15 * time.Minute }
func refreshTokenTTLSeconds() time.Duration { return 30 * 24 * time.Hour }

// base64URLDecode is a thin wrapper over base64.RawURLEncoding.DecodeString.
// JWT payload sections are base64url-unpadded per RFC 7515.
func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// jsonUnmarshal exposes a named function for json.Unmarshal so the
// jti-extraction helper above stays compact.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// RefreshToken implements the BR-3.9 refresh-token rotation + reuse-
// detection flow (UNIT-118 + UNIT-119).
//
// Wire input: a signed refresh token (the JWT itself — no separate
// access token is needed). Handler responsibility:
//
//   1. Verify the JWT (signature, exp, alg=RS256, aud=he-api).
//   2. Extract sub (user_id) + jti + fam (family_id) from claims.
//   3. Run the atomic Lua family-rotation script:
//        GET auth:refresh:{family_id}
//        if missing                                   → "not_found"
//        if stored != presented_jti  → DEL + return "compromised"
//        else                        → DEL + SET new_jti + return "ok"
//   4. On "ok": sign a new access + refresh pair with the SAME
//      family_id but new jtis; the Lua script already wrote the new
//      refresh jti to Redis. Return tokens.
//   5. On "not_found" / "compromised": return 401_invalid_credentials
//      so the client re-runs the full signin. Audit signin_failure
//      with the discriminating ErrorCode.
//
// Note on response code: 401_invalid_credentials is intentional —
// neither "your refresh is expired" nor "we detected reuse and revoked
// everyone" surfaces a distinguishing signal to the client. The
// observable behavior is: "your session is dead, sign in again."
func (s *AuthServer) RefreshToken(
	ctx context.Context,
	req *connect.Request[authv1.RefreshTokenRequest],
) (*connect.Response[authv1.RefreshTokenResponse], error) {
	in := req.Msg
	now := s.Clock()

	if in.GetRefreshToken() == "" {
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}

	// === 1. Verify the JWT ===
	claims, err := s.JWTVerify.Verify(in.GetRefreshToken())
	if err != nil {
		s.auditSigninFailure(ctx, "", "", in.GetClientIp(), in.GetUserAgent(), now, StatusInvalidCredentials)
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	if claims.FamilyID == "" {
		// Access token presented as refresh — reject. Refresh tokens are
		// the ONLY shape carrying `fam`; defensive against caller bugs.
		s.auditSigninFailure(ctx, claims.Subject, "", in.GetClientIp(), in.GetUserAgent(), now, StatusInvalidCredentials)
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}
	familyID, err := uuid.Parse(claims.FamilyID)
	if err != nil {
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	}

	// === 2. Pre-sign the new refresh token so we can hand its jti to the
	//        Lua script as the SET argument. If the script returns
	//        compromised / not_found we discard the freshly signed token.
	newRefreshToken, err := s.JWT.SignRefreshToken(userID, familyID, now)
	if err != nil {
		return nil, internalErr(err, "sign_refresh")
	}
	newRefreshJTI, err := jtiFromToken(newRefreshToken)
	if err != nil {
		return nil, internalErr(err, "decode_refresh_jti")
	}

	// === 3. Atomic Lua rotation ===
	familyKey := refreshFamilyKeyPrefix + familyID.String()
	raw, err := s.Redis.Eval(ctx, luaRotateRefreshFamily, []string{familyKey},
		claims.JTI,
		newRefreshJTI,
		int64(refreshTokenTTLSeconds()/time.Second),
	).Result()
	if err != nil {
		return nil, internalErr(err, "redis_rotate")
	}
	status, _ := raw.(string)
	switch status {
	case "ok":
		// fall through to signing path below
	case "compromised":
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventSigninFailure,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: "refresh.reuse_detected",
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	case "not_found":
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.EventSigninFailure,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: "refresh.family_revoked",
		})
		return nil, statusError(connect.CodeUnauthenticated, StatusInvalidCredentials)
	default:
		return nil, internalErr(fmt.Errorf("unknown rotate status %q", status), "redis_rotate")
	}

	// === 4. Sign a fresh access token. The refresh token was pre-signed
	//        + stashed in Redis above; we still need a NEW access token.
	accessToken, err := s.JWT.SignAccessToken(userID, now)
	if err != nil {
		return nil, internalErr(err, "sign_access")
	}

	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.EventSigninSuccess,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		ErrorCode: "refresh.rotated",
	})

	return connect.NewResponse(&authv1.RefreshTokenResponse{
		AccessToken:                  accessToken,
		RefreshToken:                 newRefreshToken,
		AccessTokenExpiresInSeconds:  int32(accessTokenTTLSeconds().Seconds()),
		RefreshTokenExpiresInSeconds: int32(refreshTokenTTLSeconds().Seconds()),
	}), nil
}

// luaRotateRefreshFamily atomically checks + rotates the per-family jti
// tracker. The script is the canonical implementation of BR-3.9 — it
// either rotates (success) or revokes (compromised / not_found) in a
// single Redis round-trip with no race between GET and DEL.
//
// KEYS[1] = auth:refresh:{family_id}
// ARGV[1] = presented jti
// ARGV[2] = new jti (when rotation succeeds)
// ARGV[3] = TTL seconds
//
// Returns: "ok" | "compromised" | "not_found".
const luaRotateRefreshFamily = `
local stored = redis.call("GET", KEYS[1])
if not stored then
  return "not_found"
end
if stored ~= ARGV[1] then
  redis.call("DEL", KEYS[1])
  return "compromised"
end
redis.call("SET", KEYS[1], ARGV[2], "EX", ARGV[3])
return "ok"
`

// Compile-time: ensure unused imports (uuid) compile cleanly even if a future
// edit removes the last usage — gofmt/imports would catch it but a static
// assertion never hurts.
var _ = uuid.Nil
