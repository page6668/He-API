// Story 5.2 — UNIT-080..085 (PATCH JSON wire-shape marshal order + null).
package handlers_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// 5.2-UNIT-080 — response field order LOCKS to BR-1.13 (byte-stable).
func TestUpdateKeyResponse_FieldOrder(t *testing.T) {
	cap := "50.00"
	last := "2026-06-03T00:00:00Z"
	out := handlers.UpdateKeyResponse{
		APIKeyID:            "id",
		Name:                "n",
		KeyPrefix:           "he-AA",
		Scope:               json.RawMessage(`{}`),
		MonthlyCostCapUSD:   &cap,
		CurrentMonthCostUSD: "0",
		LastUsedAt:          &last,
		CreatedAt:           "2026-05-01T00:00:00Z",
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	order := []string{
		"api_key_id", "name", "key_prefix", "scope", "monthly_cost_cap_usd",
		"current_month_cost_usd", "last_used_at", "revoked_at", "created_at",
	}
	last_i := -1
	for _, f := range order {
		i := strings.Index(s, `"`+f+`"`)
		if i < 0 {
			t.Fatalf("field %q missing from %s", f, s)
		}
		if i < last_i {
			t.Fatalf("field %q out of order in %s", f, s)
		}
		last_i = i
	}
}

// 5.2-UNIT-081 — nil cap + nil revoked_at render JSON null (not omitted).
func TestUpdateKeyResponse_NullFields(t *testing.T) {
	out := handlers.UpdateKeyResponse{
		APIKeyID:            "id",
		Scope:               json.RawMessage(`{}`),
		CurrentMonthCostUSD: "0",
	}
	b, _ := json.Marshal(out)
	s := string(b)
	if !strings.Contains(s, `"monthly_cost_cap_usd":null`) {
		t.Fatalf("nil cap must render null: %s", s)
	}
	if !strings.Contains(s, `"revoked_at":null`) {
		t.Fatalf("nil revoked_at must render null: %s", s)
	}
}
