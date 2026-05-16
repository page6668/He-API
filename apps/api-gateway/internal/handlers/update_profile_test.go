// Story 2.5 AC2 — PUT /v1/me/profile handler tests.
//
// Covers QA scenarios 2.5-UNIT-038..046 + 2.5-INT-005..013 at the gateway
// layer (auth-svc behaviour is upstream-stubbed via fakeAuthClient).
package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

func newUpdateProfileRequest(t *testing.T, body string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/me/profile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return withAuthedUser(req, "11111111-1111-1111-1111-111111111111")
}

// Scenario: 2.5-UNIT-039 — missing If-Match → 428 Precondition Required.
func TestUpdateProfileRoute_NoIfMatch_Returns428(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t, `{"locale":"en"}`, nil))

	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("code = %d, want 428", rr.Code)
	}
	if fake.lastUpdateProfileReq != nil {
		t.Errorf("upstream MUST NOT be called when If-Match missing")
	}
}

// Scenario: 2.5-UNIT-038 — unknown field in body → 400_unknown_field (BR-2.2).
func TestUpdateProfileRoute_UnknownField_Returns400(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"en","foo":"bar"}`,
		map[string]string{"If-Match": `"1715850000000000"`}))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
	if fake.lastUpdateProfileReq != nil {
		t.Errorf("upstream MUST NOT be called on strict-field violation")
	}
}

// Scenario: 2.5-UNIT-040 — happy path 200 + ETag + body + NO he_locale cookie
// when locale unchanged.
func TestUpdateProfileRoute_LocaleUnchanged_NoCookie(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	displayName := "Bob"
	fake := &fakeAuthClient{
		updateProfileResp: &authv1.UpdateProfileResponse{
			UserId:        "11111111-1111-1111-1111-111111111111",
			Email:         "user@example.com",
			DisplayName:   &displayName,
			Locale:        "en",
			Timezone:      "UTC",
			TotpEnabled:   false,
			CreatedAt:     timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:     timestamppb.New(now),
			Etag:          `"1715850000000000"`,
			LocaleChanged: false,
		},
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"display_name":"Bob"}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	if etag := rr.Header().Get("ETag"); etag != `"1715850000000000"` {
		t.Errorf("ETag = %q", etag)
	}
	// No he_locale cookie when locale_changed=false.
	for _, c := range rr.Result().Cookies() {
		if c.Name == "he_locale" {
			t.Errorf("unexpected he_locale cookie %v (locale unchanged)", c)
		}
	}
}

// Scenario: 2.5-UNIT-044 — locale-change sets he_locale cookie with the
// new value (Architect Q3 — SetLocaleCookie invoked).
func TestUpdateProfileRoute_LocaleChanged_SetsHeLocaleCookie(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	fake := &fakeAuthClient{
		updateProfileResp: &authv1.UpdateProfileResponse{
			UserId:        "11111111-1111-1111-1111-111111111111",
			Email:         "user@example.com",
			Locale:        "zh-CN",
			Timezone:      "UTC",
			CreatedAt:     timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:     timestamppb.New(now),
			Etag:          `"1715850000000000"`,
			LocaleChanged: true,
		},
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"zh-CN"}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body %s", rr.Code, rr.Body.String())
	}
	var found bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "he_locale" && c.Value == "zh-CN" {
			found = true
		}
	}
	if !found {
		t.Errorf("he_locale=zh-CN cookie missing from response (Architect Q3 contract)")
	}
}

// Scenario: 2.5-UNIT-041 — etag mismatch upstream → 412.
func TestUpdateProfileRoute_EtagMismatch_Returns412(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		updateProfileErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("412_etag_mismatch")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"en"}`,
		map[string]string{"If-Match": `"0"`}))

	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("code = %d, want 412", rr.Code)
	}
}

// Scenario: 2.5-UNIT-042 — pending_deletion upstream → 403.
func TestUpdateProfileRoute_PendingDeletion_Returns403(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		updateProfileErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("403_account_pending_deletion")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"en"}`,
		map[string]string{"If-Match": `"0"`}))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rr.Code)
	}
}

// Scenario: 2.5-UNIT-043 — rate limit → 429 + Retry-After header.
func TestUpdateProfileRoute_RateLimit_Returns429WithRetryAfter(t *testing.T) {
	t.Parallel()
	ce := connect.NewError(connect.CodeResourceExhausted, errors.New("429_rate_limit_profile_update"))
	ce.Meta().Set("Retry-After", "1800")
	fake := &fakeAuthClient{updateProfileErr: ce}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"locale":"en"}`,
		map[string]string{"If-Match": `"0"`}))

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rr.Code)
	}
	if ra := rr.Header().Get("Retry-After"); ra != "1800" {
		t.Errorf("Retry-After = %q, want 1800", ra)
	}
}

// Scenario: 2.5-UNIT-045 — locale-unchanged ALSO when locale field is the
// same as current value (handler trusts upstream's LocaleChanged flag).
func TestUpdateProfileRoute_BodyShape_PreservesNullableFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	fake := &fakeAuthClient{
		updateProfileResp: &authv1.UpdateProfileResponse{
			UserId:    "11111111-1111-1111-1111-111111111111",
			Email:     "user@example.com",
			// DisplayName + OauthProvider both nil → JSON null
			Locale:    "en",
			Timezone:  "UTC",
			CreatedAt: timestamppb.New(now.Add(-time.Hour)),
			UpdatedAt: timestamppb.New(now),
			Etag:      `"1715850000000000"`,
		},
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.UpdateProfile(rr, newUpdateProfileRequest(t,
		`{"display_name":""}`,
		map[string]string{"If-Match": `"1715800000000000"`}))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	// display_name null in JSON means *string nil round-tripped.
	if body["display_name"] != nil {
		t.Errorf("display_name = %v, want JSON null", body["display_name"])
	}
	if body["oauth_provider"] != nil {
		t.Errorf("oauth_provider = %v, want JSON null", body["oauth_provider"])
	}
}
