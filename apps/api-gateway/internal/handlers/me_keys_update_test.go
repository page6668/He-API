// Story 5.2 — INT-001..011 (PATCH /v1/me/keys/{api_key_id} gateway handler).
// Reuses the me_keys_test.go harness (stubAuthSvc + miniredis).

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"connectrpc.com/connect"
	"errors"
)

// doUpdate drives HandleUpdate with the JWT user_id injected + the path value
// set (Go 1.22 mux populates PathValue in prod; we set it manually here).
func (h *meKeysHarness) doUpdate(apiKeyID, body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/v1/me/keys/"+apiKeyID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("api_key_id", apiKeyID)
	h.handler.HandleUpdate(rr, h.withUserID(req))
	return rr
}

func okUpdateFn(req *authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
	// Echo the patched scope back as the stored scope for assertion simplicity.
	scope := "{}"
	if req.GetScope() != nil {
		if req.GetScope().GetModelsPresent() {
			scope = `{"models":["` + strings.Join(req.GetScope().GetModels(), `","`) + `"]}`
		}
	}
	resp := &authv1.UpdateApiKeyResponse{
		ApiKeyId:            req.GetApiKeyId(),
		Name:                "Production",
		KeyPrefix:           "he-AA1BB2CC3",
		Scope:               scope,
		CurrentMonthCostUsd: "0",
		CreatedAt:           timestamppb.Now(),
	}
	if req.MonthlyCostCapUsd != nil {
		v := req.GetMonthlyCostCapUsd()
		resp.MonthlyCostCapUsd = &v
	}
	return resp, nil
}

const validKeyID = "01234567-89ab-4def-8123-456789abcdef"

func TestHandleUpdate(t *testing.T) {
	t.Run("5.2-INT-001 happy scope-only → 200 + cache-control no-store", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: okUpdateFn}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"scope":{"models":["qwen-max","deepseek-v3"]}}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cache-control=%q", rr.Header().Get("Cache-Control"))
		}
		if stub.updateHits != 1 {
			t.Fatalf("updateHits=%d want 1", stub.updateHits)
		}
	})

	t.Run("5.2-INT-002 cap-only string-decimal → 200", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: okUpdateFn}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"monthly_cost_cap_usd":"50.00"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		body := decodeMeKeysBody(t, rr)
		if body["monthly_cost_cap_usd"] != "50.00" {
			t.Fatalf("cap=%v want 50.00", body["monthly_cost_cap_usd"])
		}
	})

	t.Run("5.2-INT-003 clear cap (null) → 200", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: okUpdateFn}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"monthly_cost_cap_usd":null}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("5.2-INT-004 NotFound cross-user → 404 collapse", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: func(*authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
		}}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"scope":{"models":[]}}`)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "404_api_key_not_found") {
			t.Fatalf("body=%s", rr.Body.String())
		}
	})

	t.Run("5.2-INT-005 pending_deletion → 403", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: func(*authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("account_pending_deletion"))
		}}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"monthly_cost_cap_usd":"10.00"}`)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "403_account_pending_deletion") {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	// --- gateway-side validation (auth-svc NOT called) -----------------------

	badBodies := []struct {
		name, body, wantSubstr string
		wantCode               int
	}{
		{"5.2-INT-006 invalid CIDR", `{"scope":{"ip_whitelist":["192.168.1.1/33"]}}`, "400_invalid_request", 400},
		{"5.2-INT-006b degenerate CIDR", `{"scope":{"ip_whitelist":["0.0.0.0/0"]}}`, "400_invalid_request", 400},
		{"5.2-INT-007 unknown model", `{"scope":{"models":["nope-xyz"]}}`, "400_invalid_request", 400},
		{"5.2-INT-008 empty body", `{}`, "400_invalid_request", 400},
		{"5.2-INT-009 extra top-level field", `{"foo":"bar"}`, "400_unknown_field", 400},
		{"5.2-INT-010 nested extra field", `{"scope":{"models":[],"bogus":1}}`, "400_unknown_field", 400},
		{"5.2-INT-011 JSON-number cap", `{"monthly_cost_cap_usd":50.0}`, "400_invalid_request", 400},
		{"cap out of range", `{"monthly_cost_cap_usd":"0.00"}`, "400_invalid_request", 400},
		{"current_month_cost_usd read-only (BR-1.12)", `{"current_month_cost_usd":"5.00"}`, "400_unknown_field", 400},
		{"empty scope object (BR-1.7)", `{"scope":{}}`, "400_invalid_request", 400},
	}
	for _, tc := range badBodies {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubAuthSvc{updateFn: okUpdateFn}
			h := newMeKeysHarness(t, stub)
			rr := h.doUpdate(validKeyID, tc.body)
			if rr.Code != tc.wantCode {
				t.Fatalf("status=%d want %d (body=%s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantSubstr) {
				t.Fatalf("body=%s want substr %q", rr.Body.String(), tc.wantSubstr)
			}
			if stub.updateHits != 0 {
				t.Fatalf("auth-svc must NOT be called on gateway validation failure (hits=%d)", stub.updateHits)
			}
		})
	}

	t.Run("non-UUID path → 400", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: okUpdateFn}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate("not-a-uuid", `{"monthly_cost_cap_usd":"10.00"}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
	})
}
