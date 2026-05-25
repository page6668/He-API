// Story 5.1 — UNIT-031..035 (JSON serialization contract for me_keys responses).
// See docs/qa/assessments/5.1-test-design-20260525.md.

package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// indexBefore asserts that field a appears before field b in the marshaled
// JSON byte stream. Used by UNIT-031 + UNIT-032 to lock field-order at the
// wire boundary.
func indexBefore(t *testing.T, body []byte, a, b string) {
	t.Helper()
	ia := strings.Index(string(body), `"`+a+`"`)
	ib := strings.Index(string(body), `"`+b+`"`)
	if ia < 0 {
		t.Fatalf("field %q absent from body: %s", a, body)
	}
	if ib < 0 {
		t.Fatalf("field %q absent from body: %s", b, body)
	}
	if ia >= ib {
		t.Fatalf("field %q (at %d) should precede %q (at %d) — body: %s", a, ia, b, ib, body)
	}
}

// TestMeKeysJSONSerialization covers UNIT-031..035 (BR-1.13 + BR-3.11 field-order + BR-2.10).
// Source: T5.2 (story line 573-579) + design-doc §"Unit: REST Handler Serialization".
func TestMeKeysJSONSerialization(t *testing.T) {
	t.Run("5.1-UNIT-031 CreateKeyResponse field order byte-exact BR-1.13", func(t *testing.T) {
		// Scenario: 5.1-UNIT-031
		// Priority: P0
		// Input:    CreateKeyResponse{...} populated with all 6 fields
		// Expected: marshaled JSON has byte-order
		//           api_key_id < key_prefix < name < plaintext < created_at < warning
		//           (5 strings.Index assertions).
		// BR-1.13 field-order lock
		body, err := json.Marshal(CreateKeyResponse{
			APIKeyID:  "00000000-0000-4000-8000-000000000001",
			KeyPrefix: "he-AA1BB2CC3",
			Name:      "My First Key",
			Plaintext: "he-AA1BB2CC3DD4EE5FF6GG7HH8II9JJ0KK1LL2MM3NN4OO5",
			CreatedAt: "2026-05-25T12:00:00Z",
			Warning:   PlaintextOneTimeWarning,
		})
		if err != nil {
			t.Fatalf("Marshal err=%v", err)
		}
		indexBefore(t, body, "api_key_id", "key_prefix")
		indexBefore(t, body, "key_prefix", "name")
		indexBefore(t, body, "name", "plaintext")
		indexBefore(t, body, "plaintext", "created_at")
		indexBefore(t, body, "created_at", "warning")
	})

	t.Run("5.1-UNIT-032 RevokeKeyResponse field order byte-exact BR-3.11", func(t *testing.T) {
		// Scenario: 5.1-UNIT-032
		// Priority: P0
		// Input:    RevokeKeyResponse{...} populated
		// Expected: marshaled JSON has byte-order
		//           api_key_id < revoked_at < was_already_revoked.
		// BR-3.11
		body, err := json.Marshal(RevokeKeyResponse{
			APIKeyID:          "00000000-0000-4000-8000-000000000001",
			RevokedAt:         "2026-05-25T12:00:00Z",
			WasAlreadyRevoked: false,
		})
		if err != nil {
			t.Fatalf("Marshal err=%v", err)
		}
		indexBefore(t, body, "api_key_id", "revoked_at")
		indexBefore(t, body, "revoked_at", "was_already_revoked")
	})

	t.Run("5.1-UNIT-033 KeyEntry nullable timestamp serializes as JSON null", func(t *testing.T) {
		// Scenario: 5.1-UNIT-033
		// Priority: P1
		// Input:    KeyEntry{LastUsedAt: nil, RevokedAt: nil, MonthlyCostCapUSD: nil}
		// Expected: marshaled JSON contains "last_used_at":null, "revoked_at":null,
		//           "monthly_cost_cap_usd":null (NOT omitted, NOT empty string).
		// Dev Notes §Data Models (Go) + BR-2.10
		body, err := json.Marshal(KeyEntry{
			APIKeyID:            "00000000-0000-4000-8000-000000000001",
			Name:                "Foo",
			KeyPrefix:           "he-AAA111222",
			Scope:               json.RawMessage(`{}`),
			MonthlyCostCapUSD:   nil,
			CurrentMonthCostUSD: "0",
			LastUsedAt:          nil,
			RevokedAt:           nil,
			CreatedAt:           "2026-05-25T12:00:00Z",
		})
		if err != nil {
			t.Fatalf("Marshal err=%v", err)
		}
		bodyStr := string(body)
		for _, want := range []string{`"last_used_at":null`, `"revoked_at":null`, `"monthly_cost_cap_usd":null`} {
			if !strings.Contains(bodyStr, want) {
				t.Fatalf("body missing %q: %s", want, bodyStr)
			}
		}
	})

	t.Run("5.1-UNIT-034 KeyEntry scope pass-through JSONB", func(t *testing.T) {
		// Scenario: 5.1-UNIT-034
		// Priority: P1
		// Input:    KeyEntry{Scope: json.RawMessage(`{}`)}
		// Expected: marshaled JSON contains "scope":{} (object, NOT string, NOT null).
		// BR-2.6 scope-as-stored
		body, err := json.Marshal(KeyEntry{
			Scope:               json.RawMessage(`{}`),
			CurrentMonthCostUSD: "0",
		})
		if err != nil {
			t.Fatalf("Marshal err=%v", err)
		}
		if !strings.Contains(string(body), `"scope":{}`) {
			t.Fatalf("body missing \"scope\":{} object: %s", body)
		}
		// Negative: scope must not render as string or null.
		if strings.Contains(string(body), `"scope":""`) || strings.Contains(string(body), `"scope":null`) {
			t.Fatalf("scope rendered as string or null: %s", body)
		}
	})

	t.Run("5.1-UNIT-035 BR-2.10 string-decimal serialization", func(t *testing.T) {
		// Scenario: 5.1-UNIT-035
		// Priority: P0
		// Input:    KeyEntry{MonthlyCostCapUSD: ptr("50.00"), CurrentMonthCostUSD: "0"}
		// Expected: marshaled JSON has "monthly_cost_cap_usd":"50.00" (string) AND
		//           "current_month_cost_usd":"0" (string) — NOT JSON numbers.
		// BR-2.10
		cap := "50.00"
		body, err := json.Marshal(KeyEntry{
			MonthlyCostCapUSD:   &cap,
			CurrentMonthCostUSD: "0",
			Scope:               json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("Marshal err=%v", err)
		}
		bodyStr := string(body)
		if !strings.Contains(bodyStr, `"monthly_cost_cap_usd":"50.00"`) {
			t.Fatalf("monthly_cost_cap_usd missing or not string: %s", bodyStr)
		}
		if !strings.Contains(bodyStr, `"current_month_cost_usd":"0"`) {
			t.Fatalf("current_month_cost_usd missing or not string: %s", bodyStr)
		}
		// Negative: must NOT contain unquoted number forms.
		if strings.Contains(bodyStr, `"monthly_cost_cap_usd":50.00`) || strings.Contains(bodyStr, `"current_month_cost_usd":0`) {
			t.Fatalf("money fields rendered as JSON numbers: %s", bodyStr)
		}
	})
}
