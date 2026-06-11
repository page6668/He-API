// Story 5.2 T4.2 — JSON wire shapes for PATCH /v1/me/keys/{api_key_id}.
//
// Pointer fields on the request distinguish "absent" (don't mutate) from
// "present" (mutate, even to empty) per the BR-1.7 partial-update semantics.
// MonthlyCostCapUSD is json.RawMessage so the handler distinguishes JSON
// `null` (clear the cap) from a missing field (don't touch) from a string
// value (set) — and rejects a JSON number (BR-1.6 precision discipline).
//
// Response field declaration order LOCKS JSON marshal order per BR-1.13 —
// byte-identical to Story-5.1's KeyEntry so the Console renders both with the
// same component.

package handlers

import "encoding/json"

// UpdateKeyRequest is the inbound PATCH body. Strict-field-validation
// (DisallowUnknownFields) rejects any key other than scope + monthly_cost_cap_usd
// at the top level AND models + ip_whitelist within scope (BR-1.2, two levels).
type UpdateKeyRequest struct {
	Scope             *UpdateScopePatch `json:"scope,omitempty"`
	MonthlyCostCapUSD json.RawMessage   `json:"monthly_cost_cap_usd,omitempty"` // null vs missing vs string distinguishable
	// Story 8.4 — per-Key 内容安全严格度. Pointer distinguishes "absent" (preserve)
	// from "present" (set). Validated against {strict,default,loose} at the gateway
	// BEFORE the RPC (BR-1.2). No null/"clear" form — the column is NOT NULL.
	ContentSafetyStrictness *string `json:"content_safety_strictness,omitempty"`
}

// UpdateScopePatch carries the optional scope mutation. Pointers distinguish
// "field absent" (nil → preserve existing) from "field present and empty"
// (non-nil empty slice → clear the list).
type UpdateScopePatch struct {
	Models      *[]string `json:"models,omitempty"`
	IPWhitelist *[]string `json:"ip_whitelist,omitempty"`
}

// UpdateKeyResponse is the outbound PATCH body. Field order LOCKS marshal
// order per BR-1.13 (parity with Story-5.1 KeyEntry).
type UpdateKeyResponse struct {
	APIKeyID            string          `json:"api_key_id"`
	Name                string          `json:"name"`
	KeyPrefix           string          `json:"key_prefix"`
	Scope               json.RawMessage `json:"scope"`                  // pass-through JSONB
	MonthlyCostCapUSD   *string         `json:"monthly_cost_cap_usd"`   // string-decimal; nil → null
	CurrentMonthCostUSD string          `json:"current_month_cost_usd"` // string-decimal
	LastUsedAt          *string         `json:"last_used_at"`           // RFC 3339; nil → null
	RevokedAt           *string         `json:"revoked_at"`             // RFC 3339; nil → null
	CreatedAt           string          `json:"created_at"`
	// Story 8.4 — appended LAST (additive; existing field order unchanged) so the
	// owner observes the effective per-Key level on the read-back.
	ContentSafetyStrictness string `json:"content_safety_strictness"`
}
