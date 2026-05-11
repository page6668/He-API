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

	signupRateLimit     = 5
	signupRateWindow    = 5 * time.Minute
	dummyVerifyKeyTTL   = 1 * time.Second
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

// ---- the four other RPCs remain CodeUnimplemented stubs (P3 + P4 fills) ----

func (s *AuthServer) VerifyEmail(
	_ context.Context,
	_ *connect.Request[authv1.VerifyEmailRequest],
) (*connect.Response[authv1.VerifyEmailResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("VerifyEmail: pending P3 (T2, AC2)"))
}

func (s *AuthServer) ResendVerification(
	_ context.Context,
	_ *connect.Request[authv1.ResendVerificationRequest],
) (*connect.Response[authv1.ResendVerificationResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("ResendVerification: pending P3 (T2, AC2)"))
}

func (s *AuthServer) LoginUser(
	_ context.Context,
	_ *connect.Request[authv1.LoginUserRequest],
) (*connect.Response[authv1.LoginUserResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("LoginUser: pending P4 (T3, AC3)"))
}

func (s *AuthServer) RefreshToken(
	_ context.Context,
	_ *connect.Request[authv1.RefreshTokenRequest],
) (*connect.Response[authv1.RefreshTokenResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("RefreshToken: pending P4 (T3, AC3)"))
}

// Compile-time: ensure unused imports (uuid) compile cleanly even if a future
// edit removes the last usage — gofmt/imports would catch it but a static
// assertion never hurts.
var _ = uuid.Nil
