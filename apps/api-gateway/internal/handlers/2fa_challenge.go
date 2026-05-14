// Story 2.4 AC2 — api-gateway proxy for POST /v1/auth/2fa/challenge.
//
// Reads the he_mfa cookie (set by Signin's REQUIRES_2FA branch or by the
// OAuth callback) + JSON body {code: 6-digit}; calls auth-svc.ChallengeTOTP;
// on success, swaps cookies: SET he_access + he_refresh, CLEAR he_mfa.
//
// Failure paths leave he_mfa intact so the user can retry within its 5-min
// TTL (up to the BR-2.6 5-failure soft-lock). On 423_account_locked the
// gateway clears he_mfa defensively.
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

type challengeRequestBody struct {
	Code string `json:"code"`
}

type challengeResponseBody struct {
	Status   string `json:"status"`              // "ok"
	ReturnTo string `json:"return_to,omitempty"` // populated when auth-svc threaded a value
}

// ChallengeTOTP reads the he_mfa cookie + body, proxies to auth-svc, and
// translates the gRPC response to Set-Cookie headers + JSON body.
func (p *AuthProxy) ChallengeTOTP(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(MFACookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "401_mfa_token_invalid", "verification session missing")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body challengeRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_totp_format", "request body must include code")
		return
	}

	resp, err := p.Upstream.ChallengeTOTP(r.Context(), connect.NewRequest(&authv1.ChallengeTOTPRequest{
		MfaToken:  cookie.Value,
		Code:      body.Code,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		// On account-locked / cross-token rejection / binding mismatch /
		// invalid token: clear he_mfa so the client doesn't keep retrying
		// with a dead session. On wrong-code (which is still 401), leave
		// he_mfa intact — the user has more attempts within the 5-min TTL.
		if shouldClearMFAOnError(err) {
			ClearMFACookie(w, p.Env)
		}
		translateConnectError(w, err)
		return
	}

	// Success — swap cookies.
	SetAccessCookie(w, resp.Msg.GetAccessToken(), p.Env, int(resp.Msg.GetAccessTokenExpiresInSeconds()))
	SetRefreshCookie(w, resp.Msg.GetRefreshToken(), p.Env, int(resp.Msg.GetRefreshTokenExpiresInSeconds()))
	ClearMFACookie(w, p.Env)
	writeJSON(w, http.StatusOK, challengeResponseBody{
		Status:   "ok",
		ReturnTo: resp.Msg.GetReturnTo(),
	})
}

// shouldClearMFAOnError returns true when the error code implies the
// mfa_token is dead (account locked, binding mismatch, token invalid).
// Wrong-code (401_invalid_totp_code) leaves the cookie alone so the user
// can retry.
func shouldClearMFAOnError(err error) bool {
	msg := err.Error()
	for _, code := range []string{
		"401_mfa_token_invalid",
		"401_mfa_token_binding_mismatch",
		"401_cross_token_rejected",
		"423_account_locked",
	} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}
