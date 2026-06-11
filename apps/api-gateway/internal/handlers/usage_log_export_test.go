// Story 9.3 AC1 — api-gateway usage-log-export proxy tests.
//
// Covers the white-box P0/P1 scenarios from qa/assessments/9.3-test-design:
//   - 9.3-UNIT-001 [P0] strict-fields reject (spoofed user_id) → 400
//   - 9.3-UNIT-002 [P0] user_id taken from JWT context only, never body
//   - 9.3-UNIT-003     format=xml / absent → 400 (BR-EX-2)
//   - 9.3-UNIT-004     range_days=91 → 400 (BR-EX-3)
//   - 9.3-UNIT-005     end ≤ start → 400 (BR-EX-3)
//   - 9.3-UNIT-006     ResourceExhausted → 429_rate_limit_usage_log_export
//   - 9.3-UNIT-007     happy path → no-store + body shape (format echoed)
//   - 9.3-UNIT-008     missing JWT → 401
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

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

type fakeUsageLogExportClient struct {
	requestResp *usagelogv1.RequestUsageLogExportResponse
	requestErr  error
	currentResp *usagelogv1.GetCurrentUsageLogExportResponse
	currentErr  error
	lastRequest *usagelogv1.RequestUsageLogExportRequest
}

func (f *fakeUsageLogExportClient) RequestUsageLogExport(_ context.Context, req *connect.Request[usagelogv1.RequestUsageLogExportRequest]) (*connect.Response[usagelogv1.RequestUsageLogExportResponse], error) {
	f.lastRequest = req.Msg
	if f.requestErr != nil {
		return nil, f.requestErr
	}
	return connect.NewResponse(f.requestResp), nil
}

func (f *fakeUsageLogExportClient) GetCurrentUsageLogExport(_ context.Context, req *connect.Request[usagelogv1.GetCurrentUsageLogExportRequest]) (*connect.Response[usagelogv1.GetCurrentUsageLogExportResponse], error) {
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	return connect.NewResponse(f.currentResp), nil
}

func newExportRequest(t *testing.T, method, target, body, userID string) *http.Request {
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

// 9.3-UNIT-001 [P0] + 9.3-UNIT-002 [P0]: a spoofed user_id in the body is
// rejected by DisallowUnknownFields BEFORE any upstream call; the upstream
// only ever sees the JWT-derived user_id.
func TestRequestUsageLogExport_StrictFieldsReject_400(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageLogExportClient{}
	proxy := handlers.NewUsageLogExportProxy(fake)

	req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export",
		`{"user_id":"00000000-0000-0000-0000-000000000000","format":"csv"}`,
		"11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestUsageLogExport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "400_invalid_request") {
		t.Errorf("error_code missing: %s", rec.Body.String())
	}
	if fake.lastRequest != nil {
		t.Errorf("upstream must NOT be called on a tampered body; got %+v", fake.lastRequest)
	}
}

func TestRequestUsageLogExport_UserFromJWTOnly(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageLogExportClient{requestResp: &usagelogv1.RequestUsageLogExportResponse{
		ExportId: "44444444-4444-4444-4444-444444444444", Status: "pending", Format: "csv",
		RequestedAt: timestamppb.New(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
	}}
	proxy := handlers.NewUsageLogExportProxy(fake)

	req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export",
		`{"format":"csv"}`, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestUsageLogExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	if fake.lastRequest.GetUserId() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("upstream user_id: got %q want JWT-derived", fake.lastRequest.GetUserId())
	}
	if fake.lastRequest.GetFormat() != "csv" {
		t.Errorf("format not forwarded: got %q", fake.lastRequest.GetFormat())
	}
}

func TestRequestUsageLogExport_BadInputs_400(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"format-xml", `{"format":"xml"}`},
		{"format-absent", `{"range_days":30}`},
		{"range-too-large", `{"format":"json","range_days":91}`},
		{"range-zero", `{"format":"json","range_days":0}`},
		{"end-before-start", `{"format":"json","start":"2026-06-11T00:00:00Z","end":"2026-06-01T00:00:00Z"}`},
		{"both-range-forms", `{"format":"json","range_days":30,"start":"2026-06-01T00:00:00Z","end":"2026-06-11T00:00:00Z"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeUsageLogExportClient{}
			proxy := handlers.NewUsageLogExportProxy(fake)
			req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export", tc.body, "11111111-1111-1111-1111-111111111111")
			rec := httptest.NewRecorder()
			proxy.RequestUsageLogExport(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
			}
			if fake.lastRequest != nil {
				t.Errorf("upstream must NOT be called for invalid input")
			}
		})
	}
}

// 9.3-UNIT-006 [P0]: ResourceExhausted maps to the SEPARATE canonical code.
func TestRequestUsageLogExport_RaceLimit_429(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageLogExportClient{
		requestErr: connect.NewError(connect.CodeResourceExhausted, errors.New("rate limited")),
	}
	proxy := handlers.NewUsageLogExportProxy(fake)
	req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export", `{"format":"csv"}`, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestUsageLogExport(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "429_rate_limit_usage_log_export") {
		t.Errorf("want 429_rate_limit_usage_log_export, got %s", rec.Body.String())
	}
}

func TestRequestUsageLogExport_HappyPath_NoStore(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageLogExportClient{requestResp: &usagelogv1.RequestUsageLogExportResponse{
		ExportId: "44444444-4444-4444-4444-444444444444", Status: "pending", Format: "json",
		RequestedAt: timestamppb.New(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
	}}
	proxy := handlers.NewUsageLogExportProxy(fake)
	req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export", `{"format":"json"}`, "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.RequestUsageLogExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: got %q want no-store", rec.Header().Get("Cache-Control"))
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["format"] != "json" || body["status"] != "pending" {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestRequestUsageLogExport_MissingJWT_401(t *testing.T) {
	t.Parallel()
	proxy := handlers.NewUsageLogExportProxy(&fakeUsageLogExportClient{})
	req := newExportRequest(t, http.MethodPost, "/v1/me/usage/logs/export", `{"format":"csv"}`, "")
	rec := httptest.NewRecorder()
	proxy.RequestUsageLogExport(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

// 9.3-UNIT (no-raw-URL discipline at the proxy): GET /current echoes format +
// signed_url_expires_at but NEVER a raw signed URL (the URL is email-only).
func TestGetCurrentUsageLogExport_NoRawURL(t *testing.T) {
	t.Parallel()
	fake := &fakeUsageLogExportClient{currentResp: &usagelogv1.GetCurrentUsageLogExportResponse{
		HasCurrent: true, ExportId: "44444444-4444-4444-4444-444444444444", Status: "completed", Format: "csv",
		RequestedAt:        timestamppb.New(time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)),
		SignedUrlExpiresAt: timestamppb.New(time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)),
	}}
	proxy := handlers.NewUsageLogExportProxy(fake)
	req := newExportRequest(t, http.MethodGet, "/v1/me/usage/logs/export/current", "", "11111111-1111-1111-1111-111111111111")
	rec := httptest.NewRecorder()
	proxy.GetCurrentUsageLogExport(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Errorf("response leaked a URL (signed URL must be email-only): %s", body)
	}
	if !strings.Contains(body, `"signed_url_expires_at"`) || !strings.Contains(body, `"format":"csv"`) {
		t.Errorf("missing expected fields: %s", body)
	}
}
