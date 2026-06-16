// Story 6.5 — gateway PUT /v1/me/profile + GET /v1/me default_routing_strategy:
// three-way intent mapping (omitted/null/""/value), strict-field allow-list,
// IDOR discipline, and the GET response field.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

const authedUID = "11111111-1111-1111-1111-111111111111"

func okUpdateResp(drs *string) *authv1.UpdateProfileResponse {
	now := time.Date(2026, 6, 16, 10, 0, 0, 0, time.UTC)
	return &authv1.UpdateProfileResponse{
		UserId:                 authedUID,
		Email:                  "user@example.com",
		Locale:                 "en",
		Timezone:               "UTC",
		CreatedAt:              timestamppb.New(now.Add(-24 * time.Hour)),
		UpdatedAt:              timestamppb.New(now),
		Etag:                   `"1718532000000000"`,
		DefaultRoutingStrategy: drs,
	}
}

// Scenario: 6.5-UNIT-013 — the strict-unknown-field validator ALLOWS
// default_routing_strategy; a "cost" value forwards verbatim to auth-svc.
func TestUpdateProfileRoute_DefaultRoutingStrategy_ForwardsValue(t *testing.T) {
	t.Parallel()
	cost := "cost"
	fake := &fakeAuthClient{updateProfileResp: okUpdateResp(&cost)}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"default_routing_strategy":"cost"}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	if fake.lastUpdateProfileReq == nil || fake.lastUpdateProfileReq.GetDefaultRoutingStrategy() != "cost" {
		t.Fatalf("forwarded drs = %v, want cost", fake.lastUpdateProfileReq)
	}
	// IDOR (6.5-UNIT-014): user_id forwarded is the JWT subject, never the body.
	if fake.lastUpdateProfileReq.GetUserId() != authedUID {
		t.Errorf("forwarded user_id = %q, want JWT subject %q", fake.lastUpdateProfileReq.GetUserId(), authedUID)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["default_routing_strategy"] != "cost" {
		t.Errorf("response default_routing_strategy = %v, want cost", body["default_routing_strategy"])
	}
}

// Scenario: 6.5-UNIT-011 (gateway) — explicit JSON null → forwards "" (clear).
func TestUpdateProfileRoute_DefaultRoutingStrategy_NullClears(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{updateProfileResp: okUpdateResp(nil)}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"default_routing_strategy":null}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	if fake.lastUpdateProfileReq == nil || fake.lastUpdateProfileReq.DefaultRoutingStrategy == nil {
		t.Fatalf("null must forward a PRESENT empty-string proto field (clear), got %v", fake.lastUpdateProfileReq)
	}
	if got := fake.lastUpdateProfileReq.GetDefaultRoutingStrategy(); got != "" {
		t.Errorf("forwarded clear value = %q, want empty string", got)
	}
	// Response echoes null.
	var body map[string]json.RawMessage
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if string(body["default_routing_strategy"]) != "null" {
		t.Errorf("response default_routing_strategy = %s, want null", body["default_routing_strategy"])
	}
}

// Scenario: 6.5-BLIND-BOUNDARY-002 — empty string "" is rejected at the gateway
// (400) and NEVER forwarded (it is distinct from null=clear and omitted=unchanged).
func TestUpdateProfileRoute_DefaultRoutingStrategy_EmptyStringRejected(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"default_routing_strategy":""}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 for empty string", rr.Code)
	}
	if fake.lastUpdateProfileReq != nil {
		t.Errorf("upstream MUST NOT be called for an empty-string default_routing_strategy")
	}
}

// Scenario: 6.5-UNIT-012 (gateway) — field omitted → proto field stays nil
// (auth-svc leaves the persisted value unchanged).
func TestUpdateProfileRoute_DefaultRoutingStrategy_OmittedNotForwarded(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{updateProfileResp: okUpdateResp(nil)}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"en"}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	if fake.lastUpdateProfileReq.DefaultRoutingStrategy != nil {
		t.Errorf("omitted field must NOT be forwarded, got %v", fake.lastUpdateProfileReq.DefaultRoutingStrategy)
	}
}

// Scenario: 6.5-UNIT-015 (gateway) — GET /v1/me surfaces default_routing_strategy
// (present + nullable). A persisted value renders; absent renders JSON null.
func TestGetMeRoute_DefaultRoutingStrategy_Surfaced(t *testing.T) {
	t.Parallel()
	latency := "latency"
	now := time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)
	fake := &fakeAuthClient{getMeResp: &authv1.GetMeResponse{
		UserId:                 authedUID,
		Email:                  "user@example.com",
		Locale:                 "en",
		Timezone:               "UTC",
		CreatedAt:              timestamppb.New(now),
		UpdatedAt:              timestamppb.New(now),
		Etag:                   `"1718528400000000"`,
		DefaultRoutingStrategy: &latency,
	}}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	req := withAuthedUser(httptest.NewRequest(http.MethodGet, "/v1/me", nil), authedUID)
	p.GetMe(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["default_routing_strategy"]; !ok {
		t.Fatalf("default_routing_strategy MUST always be present in the response")
	}
	if body["default_routing_strategy"] != "latency" {
		t.Errorf("default_routing_strategy = %v, want latency", body["default_routing_strategy"])
	}
}
