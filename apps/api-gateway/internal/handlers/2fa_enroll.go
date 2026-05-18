// Story 2.4 AC1 — api-gateway proxy for /v1/auth/2fa/enroll/{init,verify}.
//
// Both endpoints are JWT-protected (aal>=1). The middleware.RequireJWT
// wrapper places the verified user_id in the request context; this handler
// reads it via middleware.UserIDFromContext and threads it into the
// auth-svc gRPC call.
//
// The response shape mirrors auth-svc EnrollTOTPInitResponse:
//   - otpauth_uri:    string
//   - qr_code_png:    base64 string (browsers consume via data: URL)
//   - recovery_codes: []string (10 codes; returned ONCE per BR-1.9)
//   - expires_at:     RFC3339 ISO timestamp
package handlers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
)

// enrollInitResponseBody is the JSON shape returned to the console.
type enrollInitResponseBody struct {
	OtpauthURI    string   `json:"otpauth_uri"`
	QRCodePNG     string   `json:"qr_code_png"`    // base64-encoded for direct embedding
	RecoveryCodes []string `json:"recovery_codes"`
	ExpiresAt     string   `json:"expires_at"`     // ISO 8601
}

// EnrollTOTPInit decodes the JSON body (currently empty — the user_id comes
// from the verified JWT in context) and calls auth-svc.
func (p *AuthProxy) EnrollTOTPInit(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		// Wrapping middleware should always have run; this is a config bug.
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_gateway_misconfigured", "JWT middleware not wired", nil)
		return
	}
	// Body is allowed to be empty for init — no fields beyond user_id needed
	// from the wire. We bound the size defensively.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	_ = r.Body.Close

	resp, err := p.Upstream.EnrollTOTPInit(r.Context(), connect.NewRequest(&authv1.EnrollTOTPInitRequest{
		UserId:    userID,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	body := enrollInitResponseBody{
		OtpauthURI:    resp.Msg.GetOtpauthUri(),
		QRCodePNG:     base64.StdEncoding.EncodeToString(resp.Msg.GetQrCodePng()),
		RecoveryCodes: resp.Msg.GetRecoveryCodes(),
		ExpiresAt:     time.Unix(resp.Msg.GetExpiresAtUnix(), 0).UTC().Format(time.RFC3339),
	}
	writeJSON(w, http.StatusOK, body)
}

// enrollVerifyRequestBody is the JSON shape the console Server Action POSTs.
type enrollVerifyRequestBody struct {
	Code                  string `json:"code"`
	AckRecoveryCodesSaved bool   `json:"ack_recovery_codes_saved"`
}

type enrollVerifyResponseBody struct {
	OK         bool   `json:"ok"`
	EnrolledAt string `json:"enrolled_at"`
}

// EnrollTOTPVerify decodes the body + threads the verified user_id into the
// auth-svc call.
func (p *AuthProxy) EnrollTOTPVerify(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		_ = openaierr.Write(w, r.Context(), http.StatusInternalServerError, "500_gateway_misconfigured", "JWT middleware not wired", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body enrollVerifyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		_ = openaierr.Write(w, r.Context(), http.StatusBadRequest, "400_invalid_totp_format", "request body must include code + ack_recovery_codes_saved", nil)
		return
	}

	resp, err := p.Upstream.EnrollTOTPVerify(r.Context(), connect.NewRequest(&authv1.EnrollTOTPVerifyRequest{
		UserId:                userID,
		Code:                  body.Code,
		AckRecoveryCodesSaved: body.AckRecoveryCodesSaved,
		ClientIp:              clientIP(r),
		UserAgent:             r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, r.Context(), err)
		return
	}
	writeJSON(w, http.StatusOK, enrollVerifyResponseBody{
		OK:         resp.Msg.GetOk(),
		EnrolledAt: time.Unix(resp.Msg.GetEnrolledAtUnix(), 0).UTC().Format(time.RFC3339),
	})
}
