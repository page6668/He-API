// Story 2.6 AC2 — api-gateway account-data handler tests.
//
// Covers a subset of QA scenarios from the 142-scenario test design:
//
//   - 2.6-UNIT-040 RequestDataExport happy path → 200 + body shape
//   - 2.6-UNIT-041 BR-2.4 body tampering (`{"user_id":"..."}`) → 400
//   - 2.6-UNIT-042 BR-2.5 race-condition 429 mapped to 429_rate_limit_gdpr_export
//   - 2.6-UNIT-043 missing JWT → 401
//   - 2.6-UNIT-044 GetCurrentExport happy path → 200 + body
//   - 2.6-UNIT-045 GetCurrentExport no-current → 200 + null body
//
// testcontainers-backed integration scenarios (2.6-INT-001..005) are out
// of unit-test scope and run on the PR.
package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// fakeNotificationClient is the minimal subset of
// notificationv1connect.NotificationServiceClient the gateway handlers use.
type fakeNotificationClient struct {
	requestResp  *notificationv1.RequestDataExportResponse
	requestErr   error
	currentResp  *notificationv1.GetCurrentExportResponse
	currentErr   error
	lastRequest  *notificationv1.RequestDataExportRequest
	lastCurrent  *notificationv1.GetCurrentExportRequest
	sendEmailErr error
}

func (f *fakeNotificationClient) SendEmail(_ context.Context, _ *connect.Request[notificationv1.SendEmailRequest]) (*connect.Response[notificationv1.SendEmailResponse], error) {
	if f.sendEmailErr != nil {
		return nil, f.sendEmailErr
	}
	return connect.NewResponse(&notificationv1.SendEmailResponse{}), nil
}

func (f *fakeNotificationClient) RequestDataExport(_ context.Context, req *connect.Request[notificationv1.RequestDataExportRequest]) (*connect.Response[notificationv1.RequestDataExportResponse], error) {
	f.lastRequest = req.Msg
	if f.requestErr != nil {
		return nil, f.requestErr
	}
	return connect.NewResponse(f.requestResp), nil
}

func (f *fakeNotificationClient) GetCurrentExport(_ context.Context, req *connect.Request[notificationv1.GetCurrentExportRequest]) (*connect.Response[notificationv1.GetCurrentExportResponse], error) {
	f.lastCurrent = req.Msg
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	return connect.NewResponse(f.currentResp), nil
}

func newProxyRequest(t *testing.T, method, target string, body string, userID string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != "" {
		r = r.WithContext(middleware.WithUserID(r.Context(), userID))
	}
	return r
}

// Scenario: 2.6-UNIT-040 — happy path: POST /v1/account/data-export returns
// 200 with body shape + Cache-Control: no-store + user_id forwarded to
// notification-svc derived from the JWT-derived context.
func TestRequestDataExport_HappyPath_200(t *testing.T) {
	t.Parallel()
	requestedAt := time.Date(2026, 5, 18, 14, 32, 11, 123000000, time.UTC)
	fake := &fakeNotificationClient{
		requestResp: &notificationv1.RequestDataExportResponse{
			ExportId:    "550e8400-e29b-41d4-a716-446655440000",
			Status:      "pending",
			RequestedAt: timestamppb.New(requestedAt),
		},
	}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodPost, "/v1/account/data-export", "", "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestDataExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control: got %q want no-store", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v (body=%s)", err, rec.Body.String())
	}
	if body["export_id"] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("export_id: got %q", body["export_id"])
	}
	if body["status"] != "pending" {
		t.Errorf("status: got %q", body["status"])
	}
	if body["requested_at"] != "2026-05-18T14:32:11.123Z" {
		t.Errorf("requested_at: got %q want 2026-05-18T14:32:11.123Z", body["requested_at"])
	}
	if fake.lastRequest == nil || fake.lastRequest.GetUserId() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("upstream user_id: got %q want JWT-derived", fake.lastRequest.GetUserId())
	}
}

// Scenario: 2.6-UNIT-041 — BR-2.4 body tampering defense: body with extra
// fields → 400 + error_code 400_invalid_request. Defense-in-depth against
// gateway middleware bypass.
func TestRequestDataExport_BodyTampering_400(t *testing.T) {
	t.Parallel()
	fake := &fakeNotificationClient{}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodPost, "/v1/account/data-export",
		`{"user_id":"00000000-0000-0000-0000-000000000000"}`,
		"11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestDataExport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "400_invalid_request") {
		t.Errorf("error_code missing: body=%s", rec.Body.String())
	}
	if fake.lastRequest != nil {
		t.Errorf("upstream should NOT be called when body invalid; got: %+v", fake.lastRequest)
	}
}

// Scenario: 2.6-UNIT-042 — BR-2.5 race-condition: notification-svc returns
// connect.CodeResourceExhausted → gateway maps to 429 +
// 429_rate_limit_gdpr_export (rest-api-spec.md §5.1.2 row).
func TestRequestDataExport_RaceLimit_429(t *testing.T) {
	t.Parallel()
	fake := &fakeNotificationClient{
		requestErr: connect.NewError(connect.CodeResourceExhausted, errors.New("ratelimit: gdpr export rate limit exceeded")),
	}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodPost, "/v1/account/data-export", "", "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestDataExport(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "429_rate_limit_gdpr_export") {
		t.Errorf("error_code missing: body=%s", rec.Body.String())
	}
}

// Scenario: 2.6-UNIT-043 — missing JWT (no user_id in context) → 401.
// Defensive — middleware.RequireJWT is the primary defense; this confirms
// the handler doesn't accidentally proceed if it's reached without auth.
func TestRequestDataExport_MissingJWT_401(t *testing.T) {
	t.Parallel()
	fake := &fakeNotificationClient{}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodPost, "/v1/account/data-export", "", "")
	rec := httptest.NewRecorder()
	proxy.RequestDataExport(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

// Scenario: 2.6-UNIT-044 — GetCurrentExport happy path: user has an
// in-flight export → 200 + body shape with signed_url_expires_at set
// when status=completed.
func TestGetCurrentExport_HasCurrent_200(t *testing.T) {
	t.Parallel()
	requestedAt := time.Date(2026, 5, 18, 14, 32, 11, 123000000, time.UTC)
	expiresAt := time.Date(2026, 5, 19, 14, 54, 32, 456000000, time.UTC)
	fake := &fakeNotificationClient{
		currentResp: &notificationv1.GetCurrentExportResponse{
			HasCurrent:         true,
			ExportId:           "550e8400-e29b-41d4-a716-446655440000",
			Status:             "completed",
			RequestedAt:        timestamppb.New(requestedAt),
			SignedUrlExpiresAt: timestamppb.New(expiresAt),
		},
	}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodGet, "/v1/account/data-export/current", "", "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.GetCurrentExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "completed" {
		t.Errorf("status: got %v", body["status"])
	}
	if body["signed_url_expires_at"] != "2026-05-19T14:54:32.456Z" {
		t.Errorf("signed_url_expires_at: got %v", body["signed_url_expires_at"])
	}
}

// Scenario: 2.6-UNIT-045 — GetCurrentExport no-current: user has never
// requested an export → 200 + body=`null` so the UI can enable the CTA.
func TestGetCurrentExport_NoCurrent_200Null(t *testing.T) {
	t.Parallel()
	fake := &fakeNotificationClient{
		currentResp: &notificationv1.GetCurrentExportResponse{HasCurrent: false},
	}
	proxy := handlers.NewAccountDataProxy(fake)

	req := newProxyRequest(t, http.MethodGet, "/v1/account/data-export/current", "", "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.GetCurrentExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "null" {
		t.Errorf("body: got %q want null", got)
	}
}
