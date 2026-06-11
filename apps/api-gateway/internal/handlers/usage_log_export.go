// Story 9.3 AC1 — api-gateway REST handlers for usage-log export.
//
//	POST /v1/me/usage/logs/export          → notification-svc.RequestUsageLogExport
//	GET  /v1/me/usage/logs/export/current  → notification-svc.GetCurrentUsageLogExport
//
// Both routes are JWT-protected (cmd/server/main.go wraps them in
// jwtVerifier.RequireJWT). user_id is taken ONLY from the verified JWT `sub`
// via middleware.UserIDFromContext (BR-EX-1 — NEVER from the body). Unlike the
// 9.2 read endpoints these proxy to notification-svc (not ClickHouse) and are
// therefore ALWAYS mounted, independent of HE_API_CLICKHOUSE_DSN (BR-EX-8).
//
// POST body (real JSON, strict-decoded per the me_keys.go / update_profile.go
// DisallowUnknownFields precedent — NOT the account_data.go empty-body pattern,
// Architect Low-1):
//
//	{ "format": "json"|"csv", "range_days"?: 1..90, "start"?: rfc3339, "end"?: rfc3339 }
//
// Any unknown field (e.g. a spoofed "user_id") → 400_invalid_request (BR-EX-1).
//
// Response: { export_id, status, format, requested_at }; Cache-Control: no-store.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	usagelogv1 "github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/usagelog/v1/usagelogv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

const maxUsageLogExportRangeDays = 90

// UsageLogExportProxy reverse-proxies /v1/me/usage/logs/export to notification-svc.
type UsageLogExportProxy struct {
	Upstream usagelogv1connect.UsageLogExportServiceClient
}

// NewUsageLogExportProxy wires the supplied upstream client. Tests inject a fake.
func NewUsageLogExportProxy(upstream usagelogv1connect.UsageLogExportServiceClient) *UsageLogExportProxy {
	return &UsageLogExportProxy{Upstream: upstream}
}

// requestLogExportBody is the strict-decoded POST request body.
type requestLogExportBody struct {
	Format    string `json:"format"`
	RangeDays *int   `json:"range_days,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
}

type usageLogExportResponseBody struct {
	ExportID    string `json:"export_id"`
	Status      string `json:"status"`
	Format      string `json:"format"`
	RequestedAt string `json:"requested_at"`
}

type currentUsageLogExportResponseBody struct {
	ExportID           string  `json:"export_id"`
	Status             string  `json:"status"`
	Format             string  `json:"format"`
	RequestedAt        string  `json:"requested_at"`
	SignedURLExpiresAt *string `json:"signed_url_expires_at"`
}

// RequestUsageLogExport implements POST /v1/me/usage/logs/export.
func (p *UsageLogExportProxy) RequestUsageLogExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "request body too large or unreadable", nil)
		return
	}
	var body requestLogExportBody
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields() // BR-EX-1 — reject spoofed user_id / any unknown field
		if err := dec.Decode(&body); err != nil {
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "unrecognized or malformed field — user_id is derived from JWT", nil)
			return
		}
	}

	// BR-EX-2 — format required, json|csv only.
	if body.Format != "json" && body.Format != "csv" {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "format must be json or csv", nil)
		return
	}

	// BR-EX-3 — resolve + validate the ≤90d window.
	rangeStart, rangeEnd, msg := resolveExportRange(body)
	if msg != "" {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", msg, nil)
		return
	}

	rpcReq := &usagelogv1.RequestUsageLogExportRequest{
		UserId: userID,
		Format: body.Format,
	}
	if !rangeStart.IsZero() {
		rpcReq.RangeStart = timestamppb.New(rangeStart)
	}
	if !rangeEnd.IsZero() {
		rpcReq.RangeEnd = timestamppb.New(rangeEnd)
	}

	resp, err := p.Upstream.RequestUsageLogExport(r.Context(), connect.NewRequest(rpcReq))
	if err != nil {
		translateUsageLogExportError(w, r.Context(), err)
		return
	}
	m := resp.Msg

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, usageLogExportResponseBody{
		ExportID:    m.GetExportId(),
		Status:      m.GetStatus(),
		Format:      m.GetFormat(),
		RequestedAt: m.GetRequestedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// GetCurrentUsageLogExport implements GET /v1/me/usage/logs/export/current.
func (p *UsageLogExportProxy) GetCurrentUsageLogExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	resp, err := p.Upstream.GetCurrentUsageLogExport(r.Context(), connect.NewRequest(&usagelogv1.GetCurrentUsageLogExportRequest{
		UserId: userID,
	}))
	if err != nil {
		translateUsageLogExportError(w, r.Context(), err)
		return
	}
	m := resp.Msg

	w.Header().Set("Cache-Control", "no-store")
	if !m.GetHasCurrent() {
		writeJSON(w, http.StatusOK, nil)
		return
	}

	out := currentUsageLogExportResponseBody{
		ExportID:    m.GetExportId(),
		Status:      m.GetStatus(),
		Format:      m.GetFormat(),
		RequestedAt: m.GetRequestedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if ts := m.GetSignedUrlExpiresAt(); ts.IsValid() && !ts.AsTime().IsZero() {
		formatted := ts.AsTime().UTC().Format("2006-01-02T15:04:05.000Z")
		out.SignedURLExpiresAt = &formatted
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveExportRange validates the body's range fields (BR-EX-3) and returns
// the resolved [start,end] window, or a non-empty validation message. Returns
// zero times when the caller specified neither range_days nor start/end — the
// notification-svc handler then defaults to the last 90 days.
func resolveExportRange(body requestLogExportBody) (time.Time, time.Time, string) {
	hasExplicit := body.Start != "" || body.End != ""
	hasRangeDays := body.RangeDays != nil

	if hasRangeDays && hasExplicit {
		return time.Time{}, time.Time{}, "specify either range_days or start/end, not both"
	}

	if hasRangeDays {
		d := *body.RangeDays
		if d < 1 || d > maxUsageLogExportRangeDays {
			return time.Time{}, time.Time{}, "range_days must be in [1,90]"
		}
		end := time.Now().UTC()
		return end.AddDate(0, 0, -d), end, ""
	}

	if hasExplicit {
		if body.Start == "" || body.End == "" {
			return time.Time{}, time.Time{}, "start and end must both be provided"
		}
		start, err := time.Parse(time.RFC3339, body.Start)
		if err != nil {
			return time.Time{}, time.Time{}, "start must be RFC3339"
		}
		end, err := time.Parse(time.RFC3339, body.End)
		if err != nil {
			return time.Time{}, time.Time{}, "end must be RFC3339"
		}
		if !end.After(start) {
			return time.Time{}, time.Time{}, "end must be after start"
		}
		if end.Sub(start) > time.Duration(maxUsageLogExportRangeDays)*24*time.Hour {
			return time.Time{}, time.Time{}, "range span must be ≤ 90 days"
		}
		return start.UTC(), end.UTC(), ""
	}

	// Neither specified — defer to the notification-svc 90d default.
	return time.Time{}, time.Time{}, ""
}

// translateUsageLogExportError maps connect.Code → HTTP status + canonical
// error_code. The ResourceExhausted path emits 429_rate_limit_usage_log_export
// (BR-EX-5; SEPARATE code from gdpr_export).
func translateUsageLogExportError(w http.ResponseWriter, ctx context.Context, err error) {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		_ = openaierr.Write(w, ctx, http.StatusBadGateway, "502_notification_svc_unavailable", err.Error(), nil)
		return
	}
	switch connectErr.Code() {
	case connect.CodeInvalidArgument:
		_ = openaierr.Write(w, ctx, http.StatusBadRequest, "400_invalid_request", connectErr.Message(), nil)
	case connect.CodeUnauthenticated:
		_ = openaierr.Write(w, ctx, http.StatusUnauthorized, "401_invalid_api_key", "", nil)
	case connect.CodeResourceExhausted:
		_ = openaierr.Write(w, ctx, http.StatusTooManyRequests, "429_rate_limit_usage_log_export", "", nil)
	case connect.CodeUnavailable:
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_notification_svc_unavailable", "", nil)
	case connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusGatewayTimeout, "504_notification_svc_timeout", "", nil)
	default:
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "", nil)
	}
}
