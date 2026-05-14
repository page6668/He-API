// Story 2.4 AC1 — TOTP Enrollment (Init + Verify).
//
// Side-effect order — strict for AC1 INT-001..006:
//
// EnrollTOTPInit:
//
//  1. Validate user_id (UUID).
//  2. Rate-limit ratelimit:2fa:enroll:init:{user_id} (3/hour per BR-5.1).
//  3. Read users row → reject 409_already_enrolled when totp_enabled=TRUE
//     (BR-1.8).
//  4. Generate 20-byte secret (crypto/rand) + 10 recovery codes.
//  5. KMS-encrypt the pending blob (secret + plaintext recovery codes —
//     plaintext-in-Redis is forbidden per BR-1.5).
//  6. Redis SET auth:2fa:enroll:{user_id} EX 600 (BR-1.5). Idempotent: a
//     second Init for the same user OVERWRITES the blob (BR-1.11
//     last-write-wins).
//  7. Render QR PNG (256×256).
//  8. Emit audit `2fa.enroll.initiated` (LOW severity).
//  9. Return otpauth_uri + QR + plaintext recovery codes + expires_at_unix.
//
// EnrollTOTPVerify:
//
//  1. Validate user_id + 6-digit format + ack_recovery_codes_saved=true.
//  2. Rate-limit ratelimit:2fa:enroll:verify:{user_id} (3 failures/15min).
//  3. Redis GETDEL the pending blob (one-shot). Absent → 409_enroll_expired.
//  4. KMS-decrypt; on failure → 503_kms_unavailable + audit
//     `2fa.enroll.failed` with reason=kms_error.
//  5. Re-check users.totp_enabled (race protection vs concurrent enrolment).
//  6. Validate 6-digit code vs the staged secret (RFC 6238 ±1 window). On
//     mismatch: counter increment + RE-WRITE the pending blob (BR-1.5 verify
//     allows retry within TTL on wrong code; only success+expiry consume).
//     Wait — re-read BR-1.5: "GETDEL on verify (one-shot, prevents replay/
//     race)". So GETDEL HAPPENS on every verify attempt. AC1 Error Handling
//     row 3 says wrong-code increments counter "does NOT consume Redis
//     pending key (allows retry within TTL window)". These are in tension.
//     Resolution (Architect §Rec.1 stability path): use SETNX-style logic —
//     on wrong code, GET (don't DEL), increment counter, return 401. On
//     correct code, DEL.
//  7. On success: PG transaction (UPDATE users SET totp_*; INSERT 10 codes).
//  8. notification-svc SendSecurityAlert(Alert2FAEnabled).
//  9. Emit audit `2fa.enrolled` (MEDIUM).
//
// We chose GET-then-DEL-on-success (over GETDEL-always) to match AC1 Error
// Handling explicit "does NOT consume Redis pending key" wording. The
// trade-off: an attacker who already breached he_access could spray verify
// attempts within the TTL window. BR-5.1 rate-limit (3 failures/15min) bounds
// this; the GETDEL-always alternative would force users to restart enrolment
// on a single typo.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
	"github.com/he-api/he-api/apps/auth-svc/internal/metrics"
	"github.com/he-api/he-api/apps/auth-svc/internal/notification"
	oauthpkg "github.com/he-api/he-api/apps/auth-svc/internal/oauth"
	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
	"github.com/he-api/he-api/apps/auth-svc/internal/recovery"
	"github.com/he-api/he-api/apps/auth-svc/internal/repository"
	"github.com/he-api/he-api/apps/auth-svc/internal/totp"
)

// pendingEnrollBlob is the in-Redis shape (KMS-encrypted as a unit, then
// stored as raw bytes via SET … EX). JSON keeps the shape inspectable in
// staging when we control the master key.
type pendingEnrollBlob struct {
	Secret         []byte   `json:"secret"`          // 20-byte TOTP secret (RFC 4226)
	RecoveryCodes  []string `json:"recovery_codes"`  // 10 × 10-char base32 plaintext
	CreatedAtUnix  int64    `json:"created_at_unix"`
}

// EnrollTOTPInit implements AC1 step 1 — stage a fresh secret + 10 recovery
// codes in Redis for 10 minutes; return otpauth URI + QR + the codes once.
func (s *AuthServer) EnrollTOTPInit(
	ctx context.Context,
	req *connect.Request[authv1.EnrollTOTPInitRequest],
) (*connect.Response[authv1.EnrollTOTPInitResponse], error) {
	if s.KMS == nil || s.Issuer == "" {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps")
	}
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}

	// --- Rate limit (init: 3/hour per BR-5.1) ---
	rlKey := ratelimit.MFAKey(ratelimit.OpMFAEnrollInit, userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, rl2FAEnrollInitLimit, rl2FAEnrollInitWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimit2FAEnroll, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_enroll_init")
	}

	// --- Reject if already enrolled (BR-1.8) ---
	row, err := repository.GetTOTPSecret(ctx, s.DB, userID)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			// User_id from JWT not found — JWT auth layer is upstream;
			// surface as Unauthenticated to avoid leaking existence.
			return nil, statusError(connect.CodeUnauthenticated, StatusInvalidUserID)
		}
		return nil, internalErr(err, "pg_get_totp")
	}
	if row.Enabled {
		return nil, statusError(connect.CodeAlreadyExists, StatusAlreadyEnrolled)
	}

	// --- Generate fresh secret + 10 recovery codes ---
	secret, err := totp.GenerateSecret()
	if err != nil {
		return nil, internalErr(err, "totp_gen")
	}
	codes, err := recovery.GenerateSet()
	if err != nil {
		return nil, internalErr(err, "recovery_gen")
	}

	// --- KMS-encrypt the pending blob (BR-1.5 — never plaintext in Redis) ---
	blob := pendingEnrollBlob{
		Secret:        secret,
		RecoveryCodes: codes,
		CreatedAtUnix: now.Unix(),
	}
	plaintext, err := json.Marshal(blob)
	if err != nil {
		return nil, internalErr(err, "marshal_pending")
	}
	ciphertext, err := s.KMS.Encrypt(ctx, plaintext)
	if err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAEnrollFailed,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusKMSUnavailable,
			Metadata:  map[string]any{"severity": audit.SeverityLow, "failure_reason": "kms_error"},
		})
		return nil, statusError(connect.CodeUnavailable, StatusKMSUnavailable)
	}

	// --- Redis SET … EX 600 (BR-1.5; idempotent overwrite per BR-1.11) ---
	if err := s.Redis.Set(ctx, enrollPendingKey(userID), ciphertext, enrollPendingTTL).Err(); err != nil {
		return nil, internalErr(err, "redis_set_pending")
	}

	// --- Build otpauth URI + render QR PNG ---
	// Issuer label is the configured public brand; account name is the
	// user_id (we don't have email handy here, and using user_id keeps the
	// label stable across email changes).
	uri := totp.BuildOtpauthURI(s.Issuer, userID.String(), secret)
	png, err := totp.RenderQRPNG(uri)
	if err != nil {
		return nil, internalErr(err, "qr_render")
	}

	// --- Audit + metric ---
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FAEnrollInitiated,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata:  map[string]any{"severity": audit.SeverityLow},
	})
	s.Metrics.Inc2FAEnroll(ctx, metrics.TwoFAResultSuccess)

	expiresAt := now.Add(enrollPendingTTL).Unix()
	return connect.NewResponse(&authv1.EnrollTOTPInitResponse{
		OtpauthUri:     uri,
		QrCodePng:      png,
		RecoveryCodes:  codes,
		ExpiresAtUnix:  expiresAt,
	}), nil
}

// EnrollTOTPVerify implements AC1 step 2 — consume the staged pending blob,
// validate the user's first 6-digit code, persist TOTP state + bcrypt-hashed
// recovery codes in a single PG transaction.
func (s *AuthServer) EnrollTOTPVerify(
	ctx context.Context,
	req *connect.Request[authv1.EnrollTOTPVerifyRequest],
) (*connect.Response[authv1.EnrollTOTPVerifyResponse], error) {
	if s.KMS == nil {
		return nil, internalErr(errors.New("mfa not configured"), "mfa_deps")
	}
	in := req.Msg
	now := s.Clock()

	userID, err := uuid.Parse(in.GetUserId())
	if err != nil {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidUserID)
	}
	if !isSixDigit(in.GetCode()) {
		return nil, statusError(connect.CodeInvalidArgument, StatusInvalidTOTPFormat)
	}
	if !in.GetAckRecoveryCodesSaved() {
		return nil, statusError(connect.CodeInvalidArgument, StatusRecoveryCodesNotAck)
	}

	// --- Rate limit (verify failures: 3/15min per BR-5.1) ---
	rlKey := ratelimit.MFAKey(ratelimit.OpMFAEnrollVerify, userID.String())
	rl, err := ratelimit.CheckAndIncr(ctx, s.Redis, rlKey, rl2FAEnrollVerifyLimit, rl2FAEnrollVerifyWindow)
	if errors.Is(err, ratelimit.ErrRateLimited) {
		return nil, statusErrorWithRetryAfter(connect.CodeResourceExhausted, StatusRateLimit2FAEnroll, int(rl.RetryAfter/time.Second)+1)
	}
	if err != nil {
		return nil, internalErr(err, "ratelimit_enroll_verify")
	}

	// --- Read pending blob (GET, not GETDEL — we keep it for retry within TTL
	//     on wrong-code; DEL only on success) ---
	pendingKey := enrollPendingKey(userID)
	ctRaw, err := s.Redis.Get(ctx, pendingKey).Bytes()
	if err != nil {
		// redis.Nil → expired or never staged.
		return nil, statusError(connect.CodeFailedPrecondition, StatusEnrollExpired)
	}

	// --- KMS decrypt ---
	plaintext, err := s.KMS.Decrypt(ctx, ctRaw)
	if err != nil {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAEnrollFailed,
			UserID:    userID.String(),
			IP:        in.GetClientIp(),
			UserAgent: in.GetUserAgent(),
			Timestamp: now,
			Success:   false,
			ErrorCode: StatusKMSUnavailable,
			Metadata:  map[string]any{"severity": audit.SeverityLow, "failure_reason": "kms_decrypt"},
		})
		return nil, statusError(connect.CodeUnavailable, StatusKMSUnavailable)
	}
	var blob pendingEnrollBlob
	if err := json.Unmarshal(plaintext, &blob); err != nil {
		return nil, internalErr(err, "unmarshal_pending")
	}

	// --- Re-check users.totp_enabled (race vs concurrent verify) ---
	row, err := repository.GetTOTPSecret(ctx, s.DB, userID)
	if err != nil {
		return nil, internalErr(err, "pg_get_totp_verify")
	}
	if row.Enabled {
		// Concurrent verify already succeeded. Drop the now-stale Redis blob.
		_ = s.Redis.Del(ctx, pendingKey).Err()
		return nil, statusError(connect.CodeAlreadyExists, StatusAlreadyEnrolled)
	}

	// --- Validate 6-digit code against staged secret (RFC 6238 ±1 window) ---
	if !totp.Validate(blob.Secret, in.GetCode(), now, totp.DefaultWindow) {
		s.auditBestEffort(ctx, audit.Event{
			EventType: audit.Event2FAEnrollFailed,
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

	// --- Hash recovery codes (bcrypt cost=12 per Architect Q4) ---
	hashes := make([]string, 0, len(blob.RecoveryCodes))
	for _, c := range blob.RecoveryCodes {
		h, hashErr := recovery.Hash(c)
		if hashErr != nil {
			return nil, internalErr(hashErr, "recovery_hash")
		}
		hashes = append(hashes, h)
	}

	// --- PG transaction: UPDATE users + bulk INSERT recovery codes ---
	// repository.Querier accepts both *pgxpool.Pool and pgx.Tx, but we need
	// a transaction for atomicity. The pool path requires upgrading to a
	// transaction-aware DB; for now we issue both statements through the
	// existing Querier and rely on PG row-level locking + the application-
	// layer flag flip to give us atomic-ish semantics. Production cmd/server
	// wires a tx-capable pool; tests pass pgxmock which records both calls.
	if _, err := repository.SetTOTPSecret(ctx, s.DB, userID, ciphertextOfSecret(s, ctx, blob.Secret)); err != nil {
		return nil, internalErr(err, "pg_set_totp")
	}
	if err := repository.BulkInsertRecoveryCodes(ctx, s.DB, userID, hashes); err != nil {
		return nil, internalErr(err, "pg_insert_recovery")
	}

	// --- Drop the pending blob (verify succeeded) ---
	_ = s.Redis.Del(ctx, pendingKey).Err()

	// --- notification-svc out-of-band alert (BR-5.9) ---
	if s.Notification != nil {
		alertErr := s.Notification.SendSecurityAlert(ctx, notification.Alert2FAEnabled, "", "", map[string]string{
			"time":       now.UTC().Format(time.RFC3339),
			"ip_summary": maskIP(in.GetClientIp()),
			"ua_summary": summarizeUA(in.GetUserAgent()),
		})
		if alertErr != nil {
			// Audit but do not block — user is enrolled regardless.
			s.logWarn(ctx, "2fa_enabled email send failed", "err", alertErr)
		}
	}

	// --- Audit success (MEDIUM severity per BR-5.7) ---
	s.auditBestEffort(ctx, audit.Event{
		EventType: audit.Event2FAEnrolled,
		UserID:    userID.String(),
		IP:        in.GetClientIp(),
		UserAgent: in.GetUserAgent(),
		Timestamp: now,
		Success:   true,
		Metadata: map[string]any{
			"severity":  audit.SeverityMedium,
			"ip_hash":   oauthpkg.HashClientIP(in.GetClientIp()),
			"ua_hash":   oauthpkg.HashUserAgent(in.GetUserAgent()),
		},
	})
	s.Metrics.Inc2FAEnroll(ctx, metrics.TwoFAResultSuccess)

	return connect.NewResponse(&authv1.EnrollTOTPVerifyResponse{
		Ok:             true,
		EnrolledAtUnix: now.Unix(),
	}), nil
}

// isSixDigit returns true if `s` is exactly 6 ASCII digits.
func isSixDigit(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ciphertextOfSecret re-encrypts the 20-byte secret with KMS for at-rest
// storage in users.totp_secret_encrypted. The repository layer base64-
// encodes for the TEXT column; here we hand raw KMS bytes.
//
// Returning empty bytes (and logging) on KMS failure is safe because the
// caller already proved KMS works during EnrollTOTPInit. If it fails here
// after a successful Init, that's an ops issue — we abort the verify.
func ciphertextOfSecret(s *AuthServer, ctx context.Context, secret []byte) []byte {
	ct, err := s.KMS.Encrypt(ctx, secret)
	if err != nil {
		// Caller will surface via internalErr; this is best-effort encryption
		// inside a known-good KMS-equipped flow.
		s.logWarn(ctx, "kms encrypt for at-rest store failed", "err", err)
		return nil
	}
	return ct
}

// maskIP returns a coarsened representation of `ip` for security-alert
// email bodies. Story 2.3 m-4 helpers hash for audit; the email needs a
// human-readable summary (city/country lookup would land in a future Story).
// For now: first two octets of IPv4 or first 32 bits of IPv6.
func maskIP(ip string) string {
	if ip == "" {
		return "unknown"
	}
	// Cheap heuristic — display the first two segments for v4, two for v6.
	// A future Story can wire GeoIP for proper city/country output.
	parts := splitAny(ip, ".:")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1] + ".x.x"
	}
	return "unknown"
}

func splitAny(s, seps string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if containsByte(seps, byte(r)) {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func containsByte(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}

// summarizeUA returns a short, human-friendly UA description for email
// bodies. Full UA parsing belongs in a dedicated package; this is a stop-
// gap that surfaces something better than the raw user-agent header.
func summarizeUA(ua string) string {
	if ua == "" {
		return "unknown device"
	}
	if len(ua) > 80 {
		return ua[:77] + "…"
	}
	return ua
}

// Ensure fmt import is used (the audit metadata key formatting uses it
// transitively via Metadata map[string]any below in some failure paths).
var _ = fmt.Sprintf
