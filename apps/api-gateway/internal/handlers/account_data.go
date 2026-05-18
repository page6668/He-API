// Story 2.6 AC2 — api-gateway REST handlers for GDPR data export.
//
//   POST /v1/account/data-export       → notification-svc.RequestDataExport
//   GET  /v1/account/data-export/current → notification-svc.GetCurrentExport
//
// Both routes are JWT-protected (cmd/server/main.go wraps them in
// jwtVerifier.RequireJWT). The middleware extracts user_id from the JWT
// `sub` claim and places it in r.Context via WithUserID — read here via
// middleware.UserIDFromContext. BR-2.4 defense-in-depth: we NEVER read
// user_id from the request body / query string.
//
// Request body for POST: MUST be empty `{}` or absent (BR-2.4 — no
// client-supplied fields accepted). Any extra fields → 400_invalid_request.
//
// Response shape (snake_case per coding-standards §12.3):
//
//   POST  → { "export_id": "uuid", "status": "pending|processing|completed",
//             "requested_at": "RFC3339" }
//   GET   → { "export_id": "...", "status": "...", "requested_at": "...",
//             "signed_url_expires_at": "..." | null } | null
//
// HTTP status mapping:
//   - 200 — new or idempotent-hit export (both paths)
//   - 200 + `null` body — GET, user has no current export
//   - 400 — body contained unknown fields (BR-2.4 defense-in-depth)
//   - 401 — JWT invalid / expired (handled by middleware before this handler)
//   - 429 — race-condition rate-limit safety net (BR-2.5; error_code
//           429_rate_limit_gdpr_export per rest-api-spec.md §5.1.2 row 11)
//   - 500 — PG / Redis / Kafka transport failure
package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"

	notificationv1 "github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/notification/v1/notificationv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// AccountDataProxy reverse-proxies the /v1/account/data-export surface
// to notification-svc.
type AccountDataProxy struct {
	// Upstream is the connect-go client to notification-svc. Constructed
	// at startup in cmd/server/main.go.
	Upstream notificationv1connect.NotificationServiceClient
}

// NewAccountDataProxy wires the supplied upstream client. Tests inject a
// fake.
func NewAccountDataProxy(upstream notificationv1connect.NotificationServiceClient) *AccountDataProxy {
	return &AccountDataProxy{Upstream: upstream}
}

// requestDataExportResponseBody is the JSON envelope for POST. Fields match
// the gRPC RequestDataExportResponse shape, snake_case at the wire.
type requestDataExportResponseBody struct {
	ExportID    string `json:"export_id"`
	Status      string `json:"status"`
	RequestedAt string `json:"requested_at"` // RFC3339 / ISO-8601
}

// getCurrentExportResponseBody is the JSON envelope for GET. Encoded as
// `null` when the user has no current export (HasCurrent=false on the
// gRPC response).
type getCurrentExportResponseBody struct {
	ExportID           string  `json:"export_id"`
	Status             string  `json:"status"`
	RequestedAt        string  `json:"requested_at"`
	SignedURLExpiresAt *string `json:"signed_url_expires_at"`
}

// RequestDataExport implements POST /v1/account/data-export.
//
// BR-2.4: the body MUST be empty `{}` or absent; any extra field is a
// defense-in-depth tampering attempt and returns 400_invalid_request.
func (p *AccountDataProxy) RequestDataExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	// BR-2.4 — reject any body other than empty `{}` / no body. The
	// strict-fields decoder (DisallowUnknownFields) catches `{"user_id":
	// "..."}` etc., which would be a tampering attempt.
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "request body too large or unreadable", nil)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("{}")) {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_request", "request body must be empty or `{}` — user_id is derived from JWT", nil)
		return
	}

	resp, err := p.Upstream.RequestDataExport(r.Context(), connect.NewRequest(&notificationv1.RequestDataExportRequest{
		UserId: userID,
		// idempotency_window_seconds intentionally left at zero — the
		// notification-svc handler defaults to 86400 per BR-2.5.
	}))
	if err != nil {
		translateAccountDataError(w, r.Context(), err)
		return
	}
	msg := resp.Msg

	// BR-1.7-equivalent — exports are user-private; never cached.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, requestDataExportResponseBody{
		ExportID:    msg.GetExportId(),
		Status:      msg.GetStatus(),
		RequestedAt: msg.GetRequestedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// GetCurrentExport implements GET /v1/account/data-export/current.
// Returns `null` (200) when the user has no in-flight or recent-completed
// export — the UI uses this to enable / disable the CTA per BR-1.5.
func (p *AccountDataProxy) GetCurrentExport(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	resp, err := p.Upstream.GetCurrentExport(r.Context(), connect.NewRequest(&notificationv1.GetCurrentExportRequest{
		UserId: userID,
	}))
	if err != nil {
		translateAccountDataError(w, r.Context(), err)
		return
	}
	msg := resp.Msg

	w.Header().Set("Cache-Control", "no-store")
	if !msg.GetHasCurrent() {
		writeJSON(w, http.StatusOK, nil)
		return
	}

	out := getCurrentExportResponseBody{
		ExportID:    msg.GetExportId(),
		Status:      msg.GetStatus(),
		RequestedAt: msg.GetRequestedAt().AsTime().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if ts := msg.GetSignedUrlExpiresAt(); ts.IsValid() && !ts.AsTime().IsZero() {
		formatted := ts.AsTime().UTC().Format("2006-01-02T15:04:05.000Z")
		out.SignedURLExpiresAt = &formatted
	}
	writeJSON(w, http.StatusOK, out)
}

// translateAccountDataError maps connect.Code to HTTP status + canonical
// error_code. Mirrors translateConnectError (auth.go) but specialized so
// the BR-2.5 race-condition path emits the canonical
// 429_rate_limit_gdpr_export code registered in rest-api-spec.md §5.1.2.
func translateAccountDataError(w http.ResponseWriter, ctx context.Context, err error) {
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
		// BR-2.5 race-condition safety-net.
		_ = openaierr.Write(w, ctx, http.StatusTooManyRequests, "429_rate_limit_gdpr_export", "", nil)
	case connect.CodeUnavailable:
		_ = openaierr.Write(w, ctx, http.StatusServiceUnavailable, "503_notification_svc_unavailable", "", nil)
	case connect.CodeDeadlineExceeded:
		_ = openaierr.Write(w, ctx, http.StatusGatewayTimeout, "504_notification_svc_timeout", "", nil)
	default:
		_ = openaierr.Write(w, ctx, http.StatusInternalServerError, "500_internal_error", "", nil)
	}
}
