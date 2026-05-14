// Story 2.4 AC3 — api-gateway proxies for the recovery-codes endpoints:
//   POST /v1/auth/2fa/recovery-codes/use        — uses he_mfa cookie
//   POST /v1/auth/2fa/recovery-codes/regenerate — uses he_access cookie (aal=2)
//
// Path naming follows Architect Round 1 m-1 ruling: both endpoints share the
// plural-noun resource (`recovery-codes`) + explicit verb suffix.
package handlers

import (
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// useRecoveryCodeRequestBody — body posted by the console challenge page when
// the user toggles to the recovery-code panel.
type useRecoveryCodeRequestBody struct {
	Code string `json:"code"`
}

// useRecoveryCodeResponseBody mirrors UseRecoveryCodeResponse but exposes
// only the user-facing fields (tokens flow via cookies).
type useRecoveryCodeResponseBody struct {
	Status                 string `json:"status"`             // "ok"
	ReturnTo               string `json:"return_to,omitempty"`
	RecoveryCodesRemaining int32  `json:"recovery_codes_remaining"`
	RecoveryCodesLow       bool   `json:"recovery_codes_low"`
}

// UseRecoveryCode reads he_mfa cookie + body, proxies to auth-svc, and on
// success swaps cookies (SET he_access + he_refresh, CLEAR he_mfa).
func (p *AuthProxy) UseRecoveryCode(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(MFACookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "401_mfa_token_invalid", "verification session missing")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body useRecoveryCodeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_recovery_code_format", "request body must include code")
		return
	}

	resp, err := p.Upstream.UseRecoveryCode(r.Context(), connect.NewRequest(&authv1.UseRecoveryCodeRequest{
		MfaToken:  cookie.Value,
		Code:      body.Code,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		// Same cookie-clearing policy as ChallengeTOTP (clears on lock /
		// binding / cross-token; preserves on plain wrong-code).
		if shouldClearMFAOnError(err) {
			ClearMFACookie(w, p.Env)
		}
		translateConnectError(w, err)
		return
	}

	SetAccessCookie(w, resp.Msg.GetAccessToken(), p.Env, int(resp.Msg.GetAccessTokenExpiresInSeconds()))
	SetRefreshCookie(w, resp.Msg.GetRefreshToken(), p.Env, int(resp.Msg.GetRefreshTokenExpiresInSeconds()))
	ClearMFACookie(w, p.Env)
	writeJSON(w, http.StatusOK, useRecoveryCodeResponseBody{
		Status:                 "ok",
		ReturnTo:               resp.Msg.GetReturnTo(),
		RecoveryCodesRemaining: resp.Msg.GetRecoveryCodesRemaining(),
		RecoveryCodesLow:       resp.Msg.GetRecoveryCodesLow(),
	})
}

// regenerateRecoveryCodesRequestBody — body posted by Settings → Security.
// `factor` is "totp" | "password"; `value` is the corresponding code/password.
type regenerateRecoveryCodesRequestBody struct {
	Factor string `json:"factor"`
	Value  string `json:"value"`
}

type regenerateRecoveryCodesResponseBody struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// RegenerateRecoveryCodes is JWT-protected (aal=2 enforced via T5.2
// middleware once landed). user_id is extracted from the verified JWT
// context. The factor + value are passed through to auth-svc.
func (p *AuthProxy) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.UserIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "500_gateway_misconfigured", "JWT middleware not wired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<10)
	var body regenerateRecoveryCodesRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_factor", "request body must include factor + value")
		return
	}
	factor, err := parseFactor(body.Factor)
	if err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_factor", "factor must be 'totp' or 'password'")
		return
	}
	resp, err := p.Upstream.RegenerateRecoveryCodes(r.Context(), connect.NewRequest(&authv1.RegenerateRecoveryCodesRequest{
		UserId:    userID,
		Factor:    factor,
		Value:     body.Value,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, regenerateRecoveryCodesResponseBody{
		RecoveryCodes: resp.Msg.GetRecoveryCodes(),
	})
}

// parseFactor decodes the JSON string into the proto enum. Returns an
// error for unknown values; the handler surfaces 400_invalid_factor.
func parseFactor(raw string) (authv1.VerificationFactor, error) {
	switch raw {
	case "totp":
		return authv1.VerificationFactor_VERIFICATION_FACTOR_TOTP, nil
	case "password":
		return authv1.VerificationFactor_VERIFICATION_FACTOR_PASSWORD, nil
	default:
		return authv1.VerificationFactor_VERIFICATION_FACTOR_UNSPECIFIED, errInvalidFactor
	}
}

var errInvalidFactor = stringError("invalid factor")

type stringError string

func (e stringError) Error() string { return string(e) }
