// ALL CODES MUST APPEAR IN docs/architecture/rest-api-spec.md §5.1.2 BEFORE
// landing here. RETIRED rows (e.g., 501_streaming_not_implemented retired by
// Story 3.4) are OMITTED — preserved in the spec for git-blame only.
package openaierr

// CodeMetadata maps each canonical error code to its HTTP status and OpenAI
// error.type. Single source of truth (BR-1.4 + Architect Round 1 Medium ruling).
// Exported so tests can range over it (UNIT-003 / UNIT-014 / INT-014 matrix smoke).
//
// Taxonomy = 16 active §5.1.2 codes + non-OpenAI auth/me/2fa/oauth/account codes
// promoted into the single canonical superset. The Story spec enumerated 22
// promoted codes; Dev discovered an additional 19 codes already used by source
// handlers/middleware that the SM enumeration missed (see Story-3.6 Dev Log →
// Implementation Decisions Log: "codeMetadata superset expansion").
//
// Codes here MUST equal the set referenced from any caller in
// apps/api-gateway/internal/{handlers,middleware}; otherwise the caller will hit
// Write's unknown-code defensive remap (500_internal_error fallback) — a
// contract regression on any existing endpoint asserting that code.
var CodeMetadata = map[string]struct {
	HTTPStatus int
	ErrorType  string
}{
	// --- 16 active rows of rest-api-spec.md §5.1.2 (OpenAI-compatible surface) ---
	"400_content_filter":         {400, "invalid_request_error"},
	"400_invalid_request":        {400, "invalid_request_error"},
	"401_invalid_api_key":        {401, "invalid_request_error"},
	"402_balance_insufficient":   {402, "invalid_request_error"},
	"402_quota_exhausted":        {402, "invalid_request_error"},
	"403_ip_not_whitelisted":     {403, "invalid_request_error"},
	"403_model_not_in_scope":     {403, "invalid_request_error"},
	"413_payload_too_large":      {413, "invalid_request_error"},
	"429_rate_limit_qps":         {429, "invalid_request_error"},
	"429_rate_limit_tpm":         {429, "invalid_request_error"},
	"429_rate_limit_gdpr_export": {429, "invalid_request_error"},
	"500_gateway_misconfigured":  {500, "server_error"},
	"500_internal_error":         {500, "server_error"},
	"501_not_implemented":        {501, "server_error"},
	"502_upstream_unavailable":   {502, "server_error"},
	"504_upstream_timeout":       {504, "server_error"},
	// 501_streaming_not_implemented — RETIRED Story 3.4; OMITTED here per BR-1.4
	// + T0.2 + Architect Round 2 L1 (caller path deleted by Story 3.4 T0.4).

	// --- promoted non-OpenAI codes (Architect Round 1 Medium ruling: single
	//     canonical taxonomy; T8.1 expands §5.1.2 to mirror this superset) ---

	// auth surface (Stories 2.2 / 2.3 / 2.5)
	"400_invalid_email":         {400, "invalid_request_error"},
	"400_invalid_token":         {400, "invalid_request_error"},
	"400_password_breached":     {400, "invalid_request_error"},
	"400_invalid_body":          {400, "invalid_request_error"},
	"400_unknown_field":         {400, "invalid_request_error"},
	"400_oauth_invalid_return_to": {400, "invalid_request_error"},
	"400_oauth_state_invalid":   {400, "invalid_request_error"},
	"401_unauthenticated":       {401, "invalid_request_error"},
	"401_invalid_credentials":   {401, "invalid_request_error"},
	"401_unauthorized":          {401, "invalid_request_error"},
	"401_access_token_expired":  {401, "invalid_request_error"},
	"401_cross_token_rejected":  {401, "invalid_request_error"},
	"403_csrf_check_failed":     {403, "invalid_request_error"},
	"403_account_pending_deletion": {403, "invalid_request_error"},
	"410_token_expired":         {410, "invalid_request_error"},
	"410_token_used":            {410, "invalid_request_error"},
	"412_etag_mismatch":         {412, "invalid_request_error"},
	"423_account_locked":        {423, "invalid_request_error"},
	"428_precondition_required": {428, "invalid_request_error"},
	"429_rate_limit_signup":     {429, "invalid_request_error"},
	"429_rate_limit_resend_ip":  {429, "invalid_request_error"},
	"429_rate_limit_oauth":      {429, "invalid_request_error"},
	"429_rate_limit_profile_update": {429, "invalid_request_error"},
	"500_email_send_failed":     {500, "server_error"},
	"502_auth_svc_unavailable":  {502, "server_error"},
	"502_oauth_provider_error":  {502, "server_error"},
	"503_auth_unavailable":      {503, "server_error"},
	"503_hibp_unavailable":      {503, "server_error"},
	"503_database_unavailable":  {503, "server_error"},
	"503_jwks_unavailable":      {503, "server_error"},
	"503_service_unavailable":   {503, "server_error"},

	// 2FA surface (Story 2.4)
	"400_invalid_factor":              {400, "invalid_request_error"},
	"400_invalid_totp_format":         {400, "invalid_request_error"},
	"400_invalid_recovery_code_format": {400, "invalid_request_error"},
	"401_invalid_totp_code":           {401, "invalid_request_error"},
	"401_mfa_token_invalid":           {401, "invalid_request_error"},
	"401_mfa_token_binding_mismatch":  {401, "invalid_request_error"},
	"403_aal2_required":               {403, "invalid_request_error"},

	// account surface (Story 2.6)
	"502_notification_svc_unavailable": {502, "server_error"},
	"503_notification_svc_unavailable": {503, "server_error"},
	"504_notification_svc_timeout":     {504, "server_error"},
}
