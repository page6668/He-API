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
	"400_content_filter":       {400, "invalid_request_error"},
	"400_invalid_request":      {400, "invalid_request_error"},
	"401_invalid_api_key":      {401, "invalid_request_error"},
	"402_balance_insufficient": {402, "invalid_request_error"},
	// Story 4.7 OQ-4.7-6 ratified — NEW envelope code for the 405 path
	// introduced by the unauthenticated GET /public/models endpoint
	// (Story-3.6 §5.1.2 "one envelope code per HTTP scenario" precedent).
	"405_method_not_allowed": {405, "invalid_request_error"},
	"402_quota_exhausted":    {402, "invalid_request_error"},
	"403_ip_not_whitelisted": {403, "invalid_request_error"},
	"403_model_not_in_scope": {403, "invalid_request_error"},
	"413_payload_too_large":  {413, "invalid_request_error"},
	"429_rate_limit_qps":     {429, "invalid_request_error"},
	// Story 5.3 Architect Q5 ratified — fills the missing third member of
	// the QPS/RPM/TPM triplet (§5.1.2 "one envelope code per HTTP scenario").
	"429_rate_limit_rpm":         {429, "invalid_request_error"},
	"429_rate_limit_tpm":         {429, "invalid_request_error"},
	"429_rate_limit_gdpr_export": {429, "invalid_request_error"},
	// Story 9.3 (BR-EX-5) — usage-log export race-condition safety net. A
	// SEPARATE key namespace from gdpr_export so usage-log exports do not
	// consume the GDPR-export quota. One envelope code per HTTP scenario
	// (§5.1.2 rule); the paired rest-api-spec §5.1.2 row is the dual-write.
	"429_rate_limit_usage_log_export": {429, "invalid_request_error"},
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
	"400_invalid_email":             {400, "invalid_request_error"},
	"400_invalid_token":             {400, "invalid_request_error"},
	"400_password_breached":         {400, "invalid_request_error"},
	"400_invalid_body":              {400, "invalid_request_error"},
	"400_unknown_field":             {400, "invalid_request_error"},
	"400_oauth_invalid_return_to":   {400, "invalid_request_error"},
	"400_oauth_state_invalid":       {400, "invalid_request_error"},
	"401_unauthenticated":           {401, "invalid_request_error"},
	"401_invalid_credentials":       {401, "invalid_request_error"},
	"401_unauthorized":              {401, "invalid_request_error"},
	"401_access_token_expired":      {401, "invalid_request_error"},
	"401_cross_token_rejected":      {401, "invalid_request_error"},
	"403_csrf_check_failed":         {403, "invalid_request_error"},
	"403_account_pending_deletion":  {403, "invalid_request_error"},
	"410_token_expired":             {410, "invalid_request_error"},
	"410_token_used":                {410, "invalid_request_error"},
	"412_etag_mismatch":             {412, "invalid_request_error"},
	"423_account_locked":            {423, "invalid_request_error"},
	"428_precondition_required":     {428, "invalid_request_error"},
	"429_rate_limit_signup":         {429, "invalid_request_error"},
	"429_rate_limit_resend_ip":      {429, "invalid_request_error"},
	"429_rate_limit_oauth":          {429, "invalid_request_error"},
	"429_rate_limit_profile_update": {429, "invalid_request_error"},
	"500_email_send_failed":         {500, "server_error"},
	"502_auth_svc_unavailable":      {502, "server_error"},
	"502_oauth_provider_error":      {502, "server_error"},
	"503_auth_unavailable":          {503, "server_error"},
	"503_hibp_unavailable":          {503, "server_error"},
	"503_database_unavailable":      {503, "server_error"},
	"503_jwks_unavailable":          {503, "server_error"},
	"503_service_unavailable":       {503, "server_error"},
	// Story 9.1 (Q-CODE / M-3) — usage dashboard read over ClickHouse. The
	// canonical runtime mirror; the rest-api-spec §5.1.2 row is the paired
	// single-canonical-writer dual-write (Story-3.6 rule).
	"503_clickhouse_unavailable":    {503, "server_error"},

	// 2FA surface (Story 2.4)
	"400_invalid_factor":               {400, "invalid_request_error"},
	"400_invalid_totp_format":          {400, "invalid_request_error"},
	"400_invalid_recovery_code_format": {400, "invalid_request_error"},
	"401_invalid_totp_code":            {401, "invalid_request_error"},
	"401_mfa_token_invalid":            {401, "invalid_request_error"},
	"401_mfa_token_binding_mismatch":   {401, "invalid_request_error"},
	"403_aal2_required":                {403, "invalid_request_error"},

	// account surface (Story 2.6)
	"502_notification_svc_unavailable": {502, "server_error"},
	"503_notification_svc_unavailable": {503, "server_error"},
	"504_notification_svc_timeout":     {504, "server_error"},

	// API Key management surface (Story 5.1 — Architect Q4 ratified — 3 new
	// codes; cross-user / not-found collapse to single 404 per BR-3.2 anti-
	// enumeration). 502_auth_svc_unavailable + 403_account_pending_deletion
	// already registered above are REUSED on this surface (Architect Q-Spec-2:
	// 502 is the JWT-path precedent — 503 is bearer-path).
	"400_invalid_key_name":      {400, "invalid_request_error"},
	"404_api_key_not_found":     {404, "invalid_request_error"},
	"429_rate_limit_key_create": {429, "invalid_request_error"},

	// Multi-currency display surface (Story 7.2 — Architect M-1 ruling). The
	// display-currency selector ?currency= accepts exactly {usd, rmb}; any other
	// value fails loud (no silent USD fallback — a wrong-currency display is a
	// money defect, BR-B-5). Registered here in the runtime mirror per the
	// Story-3.6 single-canonical-writer rule, not the §5.1.2 spec table only.
	"400_unsupported_currency": {400, "invalid_request_error"},

	// Payment surface (Story 7.3 — Architect §5.1.2 ratification). Recharge /
	// subscription create + inbound webhook. A provider CreateCheckout/Subscription
	// API error surfaces as 402_payment_failed (a payment-domain failure to the
	// user), NOT a 502 — 5xx is reserved for our-own-infra faults (BR-W-5). The
	// webhook signature-verification failure (forged/replayed/tampered) is
	// 400_webhook_signature_invalid (the body is never parsed — BR-W-1).
	"400_invalid_payment_request":      {400, "invalid_request_error"},
	"400_unsupported_payment_provider": {400, "invalid_request_error"},
	"402_payment_failed":               {402, "invalid_request_error"},
	"400_webhook_signature_invalid":    {400, "invalid_request_error"},

	// Auto-recharge + saved-method + invoice surface (Story 7.7 — Architect §5.1.2
	// + Medium #2). Registered here in the runtime mirror per the Story-3.6
	// single-canonical-writer rule. 422_invalid_auto_recharge = bad threshold/amount
	// money fields; 422_invalid_payment_method = a foreign/non-existent/non-card
	// method on the auto-recharge config (cross-user-binding guard, BR-R-7);
	// 404_not_found = an invoice PDF / payment-method id that is not the caller's
	// (IDOR guard → 404 not 403, no existence disclosure, BR-I-6).
	"422_invalid_auto_recharge":  {422, "invalid_request_error"},
	"422_invalid_payment_method": {422, "invalid_request_error"},
	"404_not_found":              {404, "invalid_request_error"},
}
