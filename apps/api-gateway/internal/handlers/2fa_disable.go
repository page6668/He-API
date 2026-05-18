// Story 2.4 AC4 — api-gateway proxy for POST /v1/auth/2fa/disable.
//
// JWT-protected (aal=2 enforced by middleware once T5.2 lands; for now
// RequireJWT covers aal>=1 — the auth-svc factor re-verify provides
// defense-in-depth).
//
// On success the auth-svc state is cleared (totp_enabled=FALSE +
// recovery_codes deleted); existing aal=2 access tokens are NOT revoked
// (BR-4.6 documented gap — they expire naturally in 15 min). The console
// re-renders Settings → Security after the response.
package handlers

import (
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

type disableTOTPRequestBody struct {
	Factor string `json:"factor"`
	Value  string `json:"value"`
}

type disableTOTPResponseBody struct {
	OK bool `json:"ok"`
}

// DisableTOTP proxies the gRPC handler. user_id comes from the verified
// JWT context (RequireJWT wrapper).
func (p *AuthProxy) DisableTOTP(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_gateway_misconfigured", "JWT middleware not wired", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var body disableTOTPRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_factor", "request body must include factor + value", nil)
		return
	}
	factor, err := parseFactor(body.Factor)
	if err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_factor", "factor must be 'totp' or 'password'", nil)
		return
	}
	resp, err := p.Upstream.DisableTOTP(r.Context(), connect.NewRequest(&authv1.DisableTOTPRequest{
		UserId:    userID,
		Factor:    factor,
		Value:     body.Value,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	writeJSON(w, http.StatusOK, disableTOTPResponseBody{OK: resp.Msg.GetOk()})
}
