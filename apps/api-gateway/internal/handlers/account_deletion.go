// Story 2.7 — api-gateway REST handlers for GDPR account deletion.
//
//	POST /v1/account/deletion        → auth-svc.RequestAccountDeletion (AC2)
//	POST /v1/account/deletion/cancel → auth-svc.CancelAccountDeletion  (AC3)
//	GET  /v1/account/deletion        → auth-svc.GetAccountDeletionState (AC1/3/4)
//
// All three are JWT-protected (cmd/server wraps them in RequireJWT). user_id is
// the JWT `sub` claim read via middleware.UserIDFromContext — NEVER from the
// body/query (BR-2.4 IDOR defence). The POST /deletion body carries ONLY a
// `reauth` object; any other field (notably `user_id`) → 400_invalid_body
// (UNIT-015 / SEC-002). auth-svc encodes the canonical error code in the connect
// error message; translateConnectError (auth.go) maps it to HTTP + Retry-After.
package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// AccountDeletionProxy reverse-proxies the /v1/account/deletion surface to
// auth-svc.
type AccountDeletionProxy struct {
	Upstream authv1connect.AuthServiceClient
}

// NewAccountDeletionProxy wires the supplied auth-svc client. Tests inject a fake.
func NewAccountDeletionProxy(upstream authv1connect.AuthServiceClient) *AccountDeletionProxy {
	return &AccountDeletionProxy{Upstream: upstream}
}

// reauthBody is the POST /v1/account/deletion request body. ONLY reauth fields
// are accepted; DisallowUnknownFields rejects a body carrying user_id etc.
type reauthBody struct {
	Reauth *struct {
		Password     *string `json:"password,omitempty"`
		ConfirmEmail *string `json:"confirm_email,omitempty"`
		TotpCode     *string `json:"totp_code,omitempty"`
	} `json:"reauth,omitempty"`
}

type deletionStateBody struct {
	Status            string  `json:"status"`
	PendingDeletionAt *string `json:"pending_deletion_at"`
	HasPassword       bool    `json:"has_password"`
	TotpEnabled       bool    `json:"totp_enabled"`
	Timezone          string  `json:"timezone"`
}

type requestDeletionResponseBody struct {
	Status            string `json:"status"`
	PendingDeletionAt string `json:"pending_deletion_at"`
	CanCancelUntil    string `json:"can_cancel_until"`
}

const isoMillis = "2006-01-02T15:04:05.000Z"

// RequestDeletion implements POST /v1/account/deletion (AC2).
func (p *AccountDeletionProxy) RequestDeletion(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}

	// Strict-decode the body: a `reauth` object only. user_id (or any other
	// field) → unknown field → 400_invalid_body (BR-2.4 defense-in-depth).
	var body reauthBody
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_body", "request body too large or unreadable", nil)
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_body", "user_id is derived from JWT — body accepts only a reauth object", nil)
			return
		}
	}

	reauth := &authv1.ReauthCredentials{}
	if body.Reauth != nil {
		reauth.Password = body.Reauth.Password
		reauth.ConfirmEmail = body.Reauth.ConfirmEmail
		reauth.TotpCode = body.Reauth.TotpCode
	}

	resp, err := p.Upstream.RequestAccountDeletion(r.Context(), connect.NewRequest(&authv1.RequestAccountDeletionRequest{
		UserId:    userID,
		Reauth:    reauth,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	msg := resp.Msg
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, requestDeletionResponseBody{
		Status:            msg.GetStatus(),
		PendingDeletionAt: msg.GetPendingDeletionAt().AsTime().UTC().Format(isoMillis),
		CanCancelUntil:    msg.GetCanCancelUntil().AsTime().UTC().Format(isoMillis),
	})
}

// CancelDeletion implements POST /v1/account/deletion/cancel (AC3).
func (p *AccountDeletionProxy) CancelDeletion(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}
	// Body MUST be empty / `{}` (no client-supplied fields — BR-2.4).
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("{}")) {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_body", "request body must be empty or `{}`", nil)
		return
	}

	resp, err := p.Upstream.CancelAccountDeletion(r.Context(), connect.NewRequest(&authv1.CancelAccountDeletionRequest{
		UserId:    userID,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": resp.Msg.GetStatus()})
}

// GetDeletionState implements GET /v1/account/deletion (AC1/AC3/AC4 hydration).
func (p *AccountDeletionProxy) GetDeletionState(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusUnauthorized, "401_unauthorized", "missing access token", nil)
		return
	}
	resp, err := p.Upstream.GetAccountDeletionState(r.Context(), connect.NewRequest(&authv1.GetAccountDeletionStateRequest{
		UserId: userID,
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	msg := resp.Msg
	out := deletionStateBody{
		Status:      msg.GetStatus(),
		HasPassword: msg.GetHasPassword(),
		TotpEnabled: msg.GetTotpEnabled(),
		Timezone:    msg.GetTimezone(),
	}
	if ts := msg.GetPendingDeletionAt(); ts.IsValid() && !ts.AsTime().IsZero() {
		formatted := ts.AsTime().UTC().Format(isoMillis)
		out.PendingDeletionAt = &formatted
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
