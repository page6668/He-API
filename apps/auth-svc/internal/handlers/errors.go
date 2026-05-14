package handlers

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"
)

// Status codes mirror docs/architecture/rest-api-spec.md §5.1.2 (TS-CONS-014).
// api-gateway extracts the prefix (`NNN_`) to translate to HTTP status + the
// `error.code` field of the OpenAI-compatible envelope. KEEP CODE STRINGS
// EXACT — they're QA assertions in INT-005..010.
const (
	StatusInvalidEmail       = "400_invalid_email"
	StatusInvalidLocale      = "400_invalid_locale"
	StatusPasswordTooShort   = "400_password_too_short"
	StatusPasswordBreached   = "400_password_breached"
	StatusInvalidToken       = "400_invalid_token"
	StatusInvalidCredentials = "401_invalid_credentials"
	StatusEmailNotVerified   = "403_email_not_verified"
	StatusAccountSuspended   = "403_account_suspended"
	StatusCSRFCheckFailed    = "403_csrf_check_failed"
	StatusAccountDeleted     = "410_account_deleted"
	StatusTokenExpired       = "410_token_expired"
	StatusTokenUsed          = "410_token_used"
	StatusAccountLocked      = "423_account_locked"
	StatusRateLimitSignup    = "429_rate_limit_signup"
	StatusRateLimitSigninIP  = "429_rate_limit_signin_ip"
	StatusRateLimitSigninEm  = "429_rate_limit_signin_email"
	StatusRateLimitResendIP  = "429_rate_limit_resend_ip"
	StatusRateLimitResendEm  = "429_rate_limit_resend_email"
	StatusEmailSendFailed    = "500_email_send_failed"
	StatusAuthSvcUnavailable = "502_auth_svc_unavailable"
	StatusHIBPUnavailable    = "503_hibp_unavailable"

	// Story 2.3 OAuth status codes — all map 1:1 to Story 2.3 §AC1
	// Error Handling table. api-gateway sets the HTTP status from the
	// 3-digit prefix; the suffix differentiates audit reasons.
	StatusOAuthInvalidProvider    = "400_oauth_invalid_provider"
	StatusOAuthStateInvalid       = "400_oauth_state_invalid"
	StatusOAuthEmailNotVerified   = "400_oauth_email_not_verified"
	StatusOAuthInvalidReturnTo    = "400_oauth_invalid_return_to"
	StatusOAuthIDTokenInvalid     = "401_oauth_id_token_invalid"
	StatusOAuthLinkUnverified     = "403_oauth_link_unverified"
	StatusOAuthSubjectMismatch    = "409_oauth_subject_mismatch"
	StatusOAuthCrossProvider      = "409_oauth_conflict_other_provider"
	StatusOAuthProviderError      = "502_oauth_provider_error"

	// Retry-After metadata key (Connect-go header). api-gateway translates
	// this to the HTTP Retry-After response header on 429 / 423 responses.
	MetaRetryAfterSeconds = "Retry-After"

	// Story 2.4 2FA status codes — map 1:1 to Story 2.4 AC1-AC4 Error
	// Handling tables. api-gateway translates the NNN_ prefix to HTTP
	// status; the suffix identifies the audit reason.
	StatusInvalidUserID                = "400_invalid_user_id"
	StatusInvalidTOTPFormat            = "400_invalid_totp_format"
	StatusInvalidRecoveryCodeFormat    = "400_invalid_recovery_code_format"
	StatusInvalidFactor                = "400_invalid_factor"
	StatusRecoveryCodesNotAck          = "400_recovery_codes_not_acknowledged"
	StatusInvalidTOTPCode              = "401_invalid_totp_code"
	StatusInvalidRecoveryCode          = "401_invalid_recovery_code"
	StatusMFATokenInvalid              = "401_mfa_token_invalid"
	StatusMFATokenBindingMismatch      = "401_mfa_token_binding_mismatch"
	StatusAAL2Required                 = "403_aal2_required"
	StatusAlreadyEnrolled              = "409_already_enrolled"
	StatusNotEnrolled                  = "409_not_enrolled"
	StatusEnrollExpired                = "409_enroll_expired"
	StatusNoRecoveryCodes              = "410_no_recovery_codes"
	StatusRateLimit2FAEnroll           = "429_rate_limit_2fa_enroll"
	StatusRateLimit2FAChallenge        = "429_rate_limit_2fa_challenge"
	StatusRateLimit2FARecovery         = "429_rate_limit_2fa_recovery"
	StatusRateLimit2FADisable          = "429_rate_limit_2fa_disable"
	StatusKMSUnavailable               = "503_kms_unavailable"
)

// statusError builds a Connect error whose Message is exactly the status
// code string. api-gateway grep's the message for `^[0-9]{3}_` to extract
// the code into the response body envelope.
func statusError(code connect.Code, statusCode string) *connect.Error {
	return connect.NewError(code, errors.New(statusCode))
}

// statusErrorWithRetryAfter is the 429 / 423 variant. The seconds value is
// attached as Connect metadata so api-gateway can emit a real HTTP
// `Retry-After` header.
func statusErrorWithRetryAfter(code connect.Code, statusCode string, retryAfterSeconds int) *connect.Error {
	err := connect.NewError(code, errors.New(statusCode))
	err.Meta().Set(MetaRetryAfterSeconds, fmt.Sprintf("%d", retryAfterSeconds))
	return err
}

// statusErrorf is the variadic helper for codes that benefit from a detail
// string (rare — most callers stick to the canonical code).
func statusErrorf(code connect.Code, statusCode string, format string, args ...any) *connect.Error {
	msg := fmt.Sprintf(format, args...)
	return connect.NewError(code, fmt.Errorf("%s: %s", statusCode, msg))
}
