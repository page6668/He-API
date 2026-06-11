// Story 8.4 — gateway config path for content_safety_strictness on
// PATCH /v1/me/keys/{id} (8.4-UNIT-006/007 + INT-001/002). Reuses the 5.2
// meKeysHarness (stubAuthSvc + miniredis).
package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// echoStrictnessUpdateFn echoes the request's content_safety_strictness into the
// response read-back and records the last request for assertions.
func echoStrictnessUpdateFn(captured **authv1.UpdateApiKeyRequest) func(*authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
	return func(req *authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error) {
		*captured = req
		resp := &authv1.UpdateApiKeyResponse{
			ApiKeyId:            req.GetApiKeyId(),
			Name:                "Production",
			KeyPrefix:           "he-AA1BB2CC3",
			Scope:               "{}",
			CurrentMonthCostUsd: "0",
			CreatedAt:           timestamppb.Now(),
			// The persisted level == the requested one (present) or "strict" default.
			ContentSafetyStrictness: req.GetContentSafetyStrictness(),
		}
		if resp.ContentSafetyStrictness == "" {
			resp.ContentSafetyStrictness = "strict"
		}
		return resp, nil
	}
}

func TestHandleUpdate_Strictness(t *testing.T) {
	// 8.4-INT-001 — each valid level → 200, proto field carried, read-back echoes.
	for _, level := range []string{"strict", "default", "loose"} {
		t.Run("8.4-INT-001 valid level "+level, func(t *testing.T) {
			var captured *authv1.UpdateApiKeyRequest
			stub := &stubAuthSvc{updateFn: echoStrictnessUpdateFn(&captured)}
			h := newMeKeysHarness(t, stub)
			rr := h.doUpdate(validKeyID, `{"content_safety_strictness":"`+level+`"}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if captured == nil || captured.ContentSafetyStrictness == nil || *captured.ContentSafetyStrictness != level {
				t.Fatalf("proto ContentSafetyStrictness not carried as %q: %+v", level, captured)
			}
			body := decodeMeKeysBody(t, rr)
			if body["content_safety_strictness"] != level {
				t.Fatalf("read-back=%v want %q", body["content_safety_strictness"], level)
			}
		})
	}

	// 8.4-UNIT-007 / INT-002 — invalid token → 400 BEFORE the RPC, no DB side-effect.
	for _, tc := range []struct{ name, body string }{
		{"unknown token", `{"content_safety_strictness":"off"}`},
		{"empty string", `{"content_safety_strictness":""}`},
		{"severity token (not a level)", `{"content_safety_strictness":"high"}`},
		{"mixed case", `{"content_safety_strictness":"Strict"}`},
	} {
		t.Run("8.4-UNIT-007 reject "+tc.name, func(t *testing.T) {
			stub := &stubAuthSvc{updateFn: echoStrictnessUpdateFn(new(*authv1.UpdateApiKeyRequest))}
			h := newMeKeysHarness(t, stub)
			rr := h.doUpdate(validKeyID, tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "400_invalid_request") ||
				!strings.Contains(rr.Body.String(), "content_safety_strictness") {
				t.Fatalf("body=%s want 400_invalid_request + param", rr.Body.String())
			}
			if stub.updateHits != 0 {
				t.Fatalf("auth-svc UpdateApiKey called %d times — must be 0 (reject before RPC)", stub.updateHits)
			}
		})
	}

	// A JSON-number token (not a string) is also rejected before the RPC.
	t.Run("8.4-UNIT-007 reject JSON number", func(t *testing.T) {
		stub := &stubAuthSvc{updateFn: echoStrictnessUpdateFn(new(*authv1.UpdateApiKeyRequest))}
		h := newMeKeysHarness(t, stub)
		rr := h.doUpdate(validKeyID, `{"content_safety_strictness":3}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400 body=%s", rr.Code, rr.Body.String())
		}
		if stub.updateHits != 0 {
			t.Fatalf("RPC called on a malformed token")
		}
	})
}
