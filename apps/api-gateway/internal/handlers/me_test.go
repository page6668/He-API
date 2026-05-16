// Story 2.5 AC1 — GET /v1/me handler tests.
//
// Covers QA scenarios 2.5-UNIT-007..010 (RequireJWT wrap NOT exercised here
// — that's an integration-test scope; here we focus on handler behaviour
// given UserIDFromContext returns a value).
package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// withAuthedUser bundles a request context with the user_id the JWT middleware
// would normally inject. Tests bypass the middleware here.
func withAuthedUser(req *http.Request, userID string) *http.Request {
	ctx := middleware.WithUserID(req.Context(), userID)
	return req.WithContext(ctx)
}

func newGetMeRequest(t *testing.T, userID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	return withAuthedUser(req, userID)
}

// Scenario: 2.5-UNIT-008 — happy path: 200 + body shape + ETag header +
// Cache-Control: no-store.
func TestGetMeRoute_HappyPath_BodyAndHeaders(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	displayName := "Alice"
	provider := "google"
	fake := &fakeAuthClient{
		getMeResp: &authv1.GetMeResponse{
			UserId:        "11111111-1111-1111-1111-111111111111",
			Email:         "user@example.com",
			DisplayName:   &displayName,
			Locale:        "zh-CN",
			Timezone:      "Asia/Shanghai",
			TotpEnabled:   true,
			OauthProvider: &provider,
			CreatedAt:     timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:     timestamppb.New(now),
			Etag:          `"1715850000000000"`,
		},
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.GetMe(rr, newGetMeRequest(t, "11111111-1111-1111-1111-111111111111"))

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	if etag := rr.Header().Get("ETag"); etag != `"1715850000000000"` {
		t.Errorf("ETag = %q", etag)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (BR-1.7)", cc)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["email"] != "user@example.com" {
		t.Errorf("email = %v", body["email"])
	}
	if body["display_name"] != "Alice" {
		t.Errorf("display_name = %v, want Alice", body["display_name"])
	}
	if body["locale"] != "zh-CN" {
		t.Errorf("locale = %v", body["locale"])
	}
	if body["totp_enabled"] != true {
		t.Errorf("totp_enabled = %v", body["totp_enabled"])
	}
}

// Scenario: 2.5-UNIT-009 — pending_deletion → 403_account_pending_deletion.
func TestGetMeRoute_PendingDeletion_Returns403(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		getMeErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("403_account_pending_deletion")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.GetMe(rr, newGetMeRequest(t, "11111111-1111-1111-1111-111111111111"))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rr.Code)
	}
}

// Scenario: 2.5-UNIT-010 — DB unavailable → 503.
func TestGetMeRoute_DBUnavailable_Returns503(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		getMeErr: connect.NewError(connect.CodeUnavailable, errors.New("503_database_unavailable")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	p.GetMe(rr, newGetMeRequest(t, "11111111-1111-1111-1111-111111111111"))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
}

// Scenario: BR-1.1 IDOR — missing user_id (middleware bypassed) → 401.
func TestGetMeRoute_NoUserIDInContext_Returns401(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)
	rr := httptest.NewRecorder()
	// Request without user_id in context (didn't go through RequireJWT).
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	p.GetMe(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
	if fake.lastGetMeReq != nil {
		t.Errorf("upstream GetMe MUST NOT be called when middleware bypassed")
	}
	_ = context.Background()
}
