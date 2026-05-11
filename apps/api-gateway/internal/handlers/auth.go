// Package handlers exposes the REST handlers for the /v1/auth/* surface
// (Wright Round 1 Q1 ruling). Each handler reverse-proxies the upstream
// auth-svc AuthService gRPC RPC via Connect-go and translates errors to
// the OpenAI-compatible {error:{code, message, he_request_id}} envelope
// (TS-CONS-014).
//
// P2g lands the real Signup handler. VerifyEmail / ResendVerification (P3)
// + Signin / Refresh (P4) still return 501 with phase pointers — their
// upstream RPCs are themselves CodeUnimplemented in auth-svc until those
// phases land.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// AuthProxy reverse-proxies the /v1/auth/* surface to auth-svc.
type AuthProxy struct {
	// Upstream is the Connect-go client to auth-svc. Constructed at startup
	// in cmd/server/main.go from HE_API_AUTH_SVC_URL.
	Upstream authv1connect.AuthServiceClient
}

// NewAuthProxy wires the supplied upstream client. Tests inject a fake
// authv1connect.AuthServiceClient.
func NewAuthProxy(upstream authv1connect.AuthServiceClient) *AuthProxy {
	return &AuthProxy{Upstream: upstream}
}

// --- POST /v1/auth/signup ------------------------------------------------

// signupRequestBody mirrors the wire shape the console Server Action POSTs.
type signupRequestBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Locale   string `json:"locale,omitempty"`
}

// signupResponseBody is the anti-enumeration response shape returned on both
// first-time and duplicate-email signups (auth-svc enforces this at the gRPC
// layer — UNIT-037 / UNIT-044).
type signupResponseBody struct {
	Message string `json:"message"`
}

const signupSuccessMessage = "verification_email_sent"

// Signup decodes the JSON body, calls auth-svc.RegisterUser via Connect,
// and emits either the success envelope or the canonical-error envelope.
func (p *AuthProxy) Signup(w http.ResponseWriter, r *http.Request) {
	// Cap body to defend against abuse — signup payload is < 1KB.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body signupRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_email", "request body must be JSON with email + password + locale")
		return
	}

	resp, err := p.Upstream.RegisterUser(r.Context(), connect.NewRequest(&authv1.RegisterUserRequest{
		Email:     body.Email,
		Password:  body.Password,
		Locale:    body.Locale,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, err)
		return
	}
	// auth-svc returns {status: "pending_verification"} — we collapse to the
	// fixed-message shape per AC1 Scenario (anti-enumeration: no user_id, no
	// distinguishing field).
	_ = resp // status is intentionally not surfaced; presence is what matters.
	writeJSON(w, http.StatusOK, signupResponseBody{Message: signupSuccessMessage})
}

// --- GET /v1/auth/verify-email?token=… -----------------------------------

// verifyEmailResponseBody is the JSON shape the gateway emits on a successful
// verification call. Mirrors the relevant fields of authv1.VerifyEmailResponse
// with snake_case wire keys (TS-CONS-014).
type verifyEmailResponseBody struct {
	UserID          string `json:"user_id"`
	EmailVerifiedAt string `json:"email_verified_at"`
	Status          string `json:"status"`
}

// VerifyEmail reads the `token` query parameter (so the user's email-link
// click resolves even with JS disabled — the console verify-email route is
// a Server Component that awaits this endpoint) and proxies the call.
// auth-svc surfaces 400_invalid_token / 410_token_expired / 410_token_used
// distinctively; translateConnectError preserves the canonical code in the
// HTTP envelope.
func (p *AuthProxy) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if tok == "" {
		writeError(w, http.StatusBadRequest, "400_invalid_token", "token query parameter is required")
		return
	}
	resp, err := p.Upstream.VerifyEmail(r.Context(), connect.NewRequest(&authv1.VerifyEmailRequest{
		Token:     tok,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, verifyEmailResponseBody{
		UserID:          resp.Msg.GetUserId(),
		EmailVerifiedAt: resp.Msg.GetEmailVerifiedAt(),
		Status:          resp.Msg.GetStatus(),
	})
}

// --- POST /v1/auth/resend-verification -----------------------------------

// resendRequestBody mirrors the console Server Action's POST body.
type resendRequestBody struct {
	Email string `json:"email"`
}

// resendResponseBody is the canonical anti-enumeration response. Per the
// Wright Round 1 m-5 ruling, ResendVerification always emits {status:"ok"}
// on the 2xx path regardless of which auth-svc branch fired (sent /
// unknown_email / already_verified). 429 is the only distinguishing
// response shape the caller can observe.
type resendResponseBody struct {
	Status string `json:"status"`
}

// ResendVerification proxies the JSON body. auth-svc rolls malformed-email
// + unknown-email + already-verified into the same {status:"ok"} response;
// the only failure path is 429 with Retry-After (preserved via Connect
// metadata).
func (p *AuthProxy) ResendVerification(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body resendRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "400_invalid_email", "request body must be JSON with email")
		return
	}
	_, err := p.Upstream.ResendVerification(r.Context(), connect.NewRequest(&authv1.ResendVerificationRequest{
		Email:     body.Email,
		ClientIp:  clientIP(r),
		UserAgent: r.UserAgent(),
	}))
	if err != nil {
		translateConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resendResponseBody{Status: "ok"})
}

// --- stubs for the two RPCs that land in P4 ------------------------------

func (p *AuthProxy) Signin(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "501_not_implemented", "pending P4 (Story 2.2 T3, AC3)")
}

func (p *AuthProxy) Refresh(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "501_not_implemented", "pending P4 (Story 2.2 T3, AC3)")
}

// --- error / envelope helpers -------------------------------------------

// errorResponseBody is the OpenAI-compatible envelope auth-svc errors surface as.
type errorResponseBody struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code        string `json:"code"`
	Message     string `json:"message,omitempty"`
	HeRequestID string `json:"he_request_id,omitempty"`
}

// statusCodeRe matches the canonical NNN_xxx prefix auth-svc embeds in
// Connect error messages (TS-CONS-014).
var statusCodeRe = regexp.MustCompile(`^(\d{3})_[a-z_]+`)

// translateConnectError extracts the auth-svc canonical status code from
// the Connect error message + emits an HTTP envelope with the appropriate
// status. The Retry-After Connect metadata header (set by 429 / 423 paths)
// is copied to the HTTP response header so the console can show a countdown.
func translateConnectError(w http.ResponseWriter, err error) {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		writeError(w, http.StatusInternalServerError, "502_auth_svc_unavailable", err.Error())
		return
	}

	statusCode := extractStatusCode(connectErr.Message())
	httpStatus := httpStatusForCode(statusCode, connectErr.Code())

	if retryAfter := connectErr.Meta().Get("Retry-After"); retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	writeError(w, httpStatus, statusCode, "")
}

// extractStatusCode pulls the NNN_xxx prefix from `msg`. Handles two forms:
//   - "400_invalid_email"
//   - "400_invalid_email: detail message"
// Anything that doesn't match falls back to the unmodified message — caller
// downgrades to a generic 502_auth_svc_unavailable for safety.
func extractStatusCode(msg string) string {
	if m := statusCodeRe.FindString(msg); m != "" {
		return m
	}
	// Form "NNN_code: detail" — trim suffix at the first colon and re-check.
	if idx := strings.IndexByte(msg, ':'); idx > 0 && idx < 64 {
		candidate := msg[:idx]
		if statusCodeRe.MatchString(candidate) {
			return candidate
		}
	}
	return "502_auth_svc_unavailable"
}

// httpStatusForCode translates the 3-digit prefix of the canonical status
// code into an HTTP status. Falls back to mapping the Connect code if the
// prefix didn't parse (defensive — shouldn't happen with auth-svc).
func httpStatusForCode(statusCode string, connectCode connect.Code) int {
	if len(statusCode) >= 3 {
		switch statusCode[:3] {
		case "400":
			return http.StatusBadRequest
		case "401":
			return http.StatusUnauthorized
		case "403":
			return http.StatusForbidden
		case "410":
			return http.StatusGone
		case "423":
			return http.StatusLocked
		case "429":
			return http.StatusTooManyRequests
		case "500":
			return http.StatusInternalServerError
		case "502":
			return http.StatusBadGateway
		case "503":
			return http.StatusServiceUnavailable
		}
	}
	switch connectCode {
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeInternal:
		return http.StatusInternalServerError
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	}
	return http.StatusInternalServerError
}

// writeJSON / writeError serialize the response bodies.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, errorResponseBody{Error: errorBody{Code: code, Message: detail}})
}

// clientIP extracts the client IP from the X-Forwarded-For header (taking
// the leftmost value) or falls back to RemoteAddr. The api-gateway is
// fronted by the cluster LoadBalancer so X-Forwarded-For is the canonical
// source; RemoteAddr is just the fallback for local dev.
//
// Context for callers: auth-svc treats the IP as the rate-limit key; a
// missing IP here means the IP-keyed limit collapses to a single empty
// bucket (auth-svc handles this gracefully, but operators should ensure
// the gateway sees X-Forwarded-For).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if comma := strings.IndexByte(xff, ','); comma > 0 {
			return strings.TrimSpace(xff[:comma])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if colon := strings.LastIndexByte(addr, ':'); colon > 0 {
		addr = addr[:colon]
	}
	return addr
}
