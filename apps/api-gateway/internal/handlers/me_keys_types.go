// Story 5.1 T5.2 — JSON wire shapes for /v1/me/keys.
//
// Field declaration order LOCKS JSON marshal order — Go's `encoding/json`
// emits struct fields in declaration order (BR-1.13 for create; BR-3.11
// for revoke). 5.1-UNIT-031..035 grep-assert byte ordering at the wire.
//
// BR-2.10 / Architect Q-Spec-4 string-decimal contract — money fields are
// rendered as JSON strings (e.g., "50.00"), NOT JSON numbers. Stripe
// precedent; preserves NUMERIC(10,2) / NUMERIC(12,4) precision on
// round-trip (a JSON number is IEEE 754 binary float which can lose
// precision on 0.1 + 0.2).

package handlers

import "encoding/json"

// CreateKeyRequest is the inbound JSON body for POST /v1/me/keys.
// Strict-field-validation: only `name` is allowed (BR-1.2).
type CreateKeyRequest struct {
	Name string `json:"name"`
}

// CreateKeyResponse is the outbound JSON body for POST /v1/me/keys.
// Field declaration order LOCKS JSON marshal order per BR-1.13:
//
//	api_key_id → key_prefix → name → plaintext → created_at → warning
type CreateKeyResponse struct {
	APIKeyID  string `json:"api_key_id"`
	KeyPrefix string `json:"key_prefix"`
	Name      string `json:"name"`
	Plaintext string `json:"plaintext"`
	CreatedAt string `json:"created_at"` // RFC 3339 UTC
	Warning   string `json:"warning"`    // fixed English per BR-1.14
}

// PlaintextOneTimeWarning is the canonical BR-1.14 fixed-English warning.
// Localization happens at the Console rendering layer (Story 5.5); the API
// contract returns the English literal so curl / programmatic SDKs get a
// clear warning regardless of Accept-Language.
const PlaintextOneTimeWarning = "This plaintext is shown ONCE. Store it now — it cannot be retrieved later."

// KeyEntry is one row in ListKeysResponse.Data. Pointers on nullable
// fields render JSON `null` (not empty string / zero value); the proto
// `optional` fields map 1:1 here.
//
// Field declaration order parallels the proto ApiKeyEntry message.
type KeyEntry struct {
	APIKeyID            string          `json:"api_key_id"`
	Name                string          `json:"name"`
	KeyPrefix           string          `json:"key_prefix"`
	Scope               json.RawMessage `json:"scope"`                  // pass-through JSONB
	MonthlyCostCapUSD   *string         `json:"monthly_cost_cap_usd"`   // string-decimal per BR-2.10; nil → null
	CurrentMonthCostUSD string          `json:"current_month_cost_usd"` // string-decimal per BR-2.10
	LastUsedAt          *string         `json:"last_used_at"`           // RFC 3339; nil → null
	RevokedAt           *string         `json:"revoked_at"`             // RFC 3339; nil → null
	CreatedAt           string          `json:"created_at"`
	// Story 8.4 — appended LAST (additive) so the LIST/GET read path surfaces the
	// effective per-Key 内容安全 level.
	ContentSafetyStrictness string `json:"content_safety_strictness"`
}

// ListKeysResponse is the outbound JSON body for GET /v1/me/keys.
// `object="list"` mirrors the OpenAI list-resource convention.
// `Data` is NEVER nil — empty list yields `[]` (BR-2.8).
type ListKeysResponse struct {
	Object string     `json:"object"`
	Data   []KeyEntry `json:"data"`
}

// RevokeKeyResponse is the outbound JSON body for DELETE /v1/me/keys/{id}.
// Field declaration order per BR-3.11:
//
//	api_key_id → revoked_at → was_already_revoked
type RevokeKeyResponse struct {
	APIKeyID          string `json:"api_key_id"`
	RevokedAt         string `json:"revoked_at"`          // RFC 3339 UTC
	WasAlreadyRevoked bool   `json:"was_already_revoked"` // true on idempotent re-revoke
}
