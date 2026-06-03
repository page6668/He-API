package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// --- fake upstream -------------------------------------------------------

// fakeAuthClient is the minimal subset of authv1connect.AuthServiceClient
// the gateway uses. Only RegisterUser is implemented; the other methods
// return ErrFakeUnimplemented so accidental routing trips the test.
type fakeAuthClient struct {
	registerResp *authv1.RegisterUserResponse
	registerErr  error
	// VerifyEmail
	verifyResp *authv1.VerifyEmailResponse
	verifyErr  error
	// ResendVerification
	resendResp *authv1.ResendVerificationResponse
	resendErr  error
	// LoginUser (P4d)
	loginResp *authv1.LoginUserResponse
	loginErr  error
	// RefreshToken (P4d)
	refreshResp *authv1.RefreshTokenResponse
	refreshErr  error
	// Story 2.4 — TOTP 2FA (T1.3 + T2.5)
	enrollInitResp   *authv1.EnrollTOTPInitResponse
	enrollInitErr    error
	enrollVerifyResp *authv1.EnrollTOTPVerifyResponse
	enrollVerifyErr  error
	challengeResp    *authv1.ChallengeTOTPResponse
	challengeErr     error
	useRecoveryResp  *authv1.UseRecoveryCodeResponse
	useRecoveryErr   error
	regenerateResp   *authv1.RegenerateRecoveryCodesResponse
	regenerateErr    error
	disableResp      *authv1.DisableTOTPResponse
	disableErr       error
	// Story 2.5 — GetMe + UpdateProfile (T1 / T2)
	getMeResp         *authv1.GetMeResponse
	getMeErr          error
	updateProfileResp *authv1.UpdateProfileResponse
	updateProfileErr  error
	// captured inputs for assertions
	lastReq              *authv1.RegisterUserRequest
	lastVerifyReq        *authv1.VerifyEmailRequest
	lastResendReq        *authv1.ResendVerificationRequest
	lastLoginReq         *authv1.LoginUserRequest
	lastRefreshReq       *authv1.RefreshTokenRequest
	lastEnrollInitReq    *authv1.EnrollTOTPInitRequest
	lastEnrollVerifyReq  *authv1.EnrollTOTPVerifyRequest
	lastChallengeReq     *authv1.ChallengeTOTPRequest
	lastUseRecoveryReq   *authv1.UseRecoveryCodeRequest
	lastRegenerateReq    *authv1.RegenerateRecoveryCodesRequest
	lastDisableReq       *authv1.DisableTOTPRequest
	lastGetMeReq         *authv1.GetMeRequest
	lastUpdateProfileReq *authv1.UpdateProfileRequest
}

var errFakeUnimplemented = errors.New("fakeAuthClient: method not stubbed")

func (f *fakeAuthClient) RegisterUser(
	_ context.Context,
	r *connect.Request[authv1.RegisterUserRequest],
) (*connect.Response[authv1.RegisterUserResponse], error) {
	f.lastReq = r.Msg
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	if f.registerResp == nil {
		f.registerResp = &authv1.RegisterUserResponse{Status: "pending_verification"}
	}
	return connect.NewResponse(f.registerResp), nil
}

func (f *fakeAuthClient) VerifyEmail(_ context.Context, r *connect.Request[authv1.VerifyEmailRequest]) (*connect.Response[authv1.VerifyEmailResponse], error) {
	f.lastVerifyReq = r.Msg
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	if f.verifyResp == nil {
		f.verifyResp = &authv1.VerifyEmailResponse{
			UserId:          "11111111-1111-1111-1111-111111111111",
			EmailVerifiedAt: "2026-05-12T12:00:00Z",
			Status:          "email_verified",
		}
	}
	return connect.NewResponse(f.verifyResp), nil
}

func (f *fakeAuthClient) ResendVerification(_ context.Context, r *connect.Request[authv1.ResendVerificationRequest]) (*connect.Response[authv1.ResendVerificationResponse], error) {
	f.lastResendReq = r.Msg
	if f.resendErr != nil {
		return nil, f.resendErr
	}
	if f.resendResp == nil {
		f.resendResp = &authv1.ResendVerificationResponse{Status: "ok"}
	}
	return connect.NewResponse(f.resendResp), nil
}

func (f *fakeAuthClient) LoginUser(_ context.Context, r *connect.Request[authv1.LoginUserRequest]) (*connect.Response[authv1.LoginUserResponse], error) {
	f.lastLoginReq = r.Msg
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	if f.loginResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.loginResp), nil
}

func (f *fakeAuthClient) RefreshToken(_ context.Context, r *connect.Request[authv1.RefreshTokenRequest]) (*connect.Response[authv1.RefreshTokenResponse], error) {
	f.lastRefreshReq = r.Msg
	if f.refreshErr != nil {
		return nil, f.refreshErr
	}
	if f.refreshResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.refreshResp), nil
}

// Story 2.3 — OAuth stubs on the gRPC client fake. P0 only satisfies the
// interface; P6 (api-gateway HTTP handlers) populates the response-fixture
// fields and assertions.
func (f *fakeAuthClient) BeginOAuth(_ context.Context, _ *connect.Request[authv1.BeginOAuthRequest]) (*connect.Response[authv1.BeginOAuthResponse], error) {
	return nil, errFakeUnimplemented
}

func (f *fakeAuthClient) CompleteOAuth(_ context.Context, _ *connect.Request[authv1.CompleteOAuthRequest]) (*connect.Response[authv1.CompleteOAuthResponse], error) {
	return nil, errFakeUnimplemented
}

// Story 2.4 — 2FA stubs. T1.3 tests for EnrollTOTPInit / EnrollTOTPVerify
// populate the per-method response/err fields below; T2.5 / T3.3 / T4.3
// fill the remaining four.
func (f *fakeAuthClient) EnrollTOTPInit(_ context.Context, r *connect.Request[authv1.EnrollTOTPInitRequest]) (*connect.Response[authv1.EnrollTOTPInitResponse], error) {
	f.lastEnrollInitReq = r.Msg
	if f.enrollInitErr != nil {
		return nil, f.enrollInitErr
	}
	if f.enrollInitResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.enrollInitResp), nil
}

func (f *fakeAuthClient) EnrollTOTPVerify(_ context.Context, r *connect.Request[authv1.EnrollTOTPVerifyRequest]) (*connect.Response[authv1.EnrollTOTPVerifyResponse], error) {
	f.lastEnrollVerifyReq = r.Msg
	if f.enrollVerifyErr != nil {
		return nil, f.enrollVerifyErr
	}
	if f.enrollVerifyResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.enrollVerifyResp), nil
}

func (f *fakeAuthClient) ChallengeTOTP(_ context.Context, r *connect.Request[authv1.ChallengeTOTPRequest]) (*connect.Response[authv1.ChallengeTOTPResponse], error) {
	f.lastChallengeReq = r.Msg
	if f.challengeErr != nil {
		return nil, f.challengeErr
	}
	if f.challengeResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.challengeResp), nil
}

func (f *fakeAuthClient) UseRecoveryCode(_ context.Context, r *connect.Request[authv1.UseRecoveryCodeRequest]) (*connect.Response[authv1.UseRecoveryCodeResponse], error) {
	f.lastUseRecoveryReq = r.Msg
	if f.useRecoveryErr != nil {
		return nil, f.useRecoveryErr
	}
	if f.useRecoveryResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.useRecoveryResp), nil
}

func (f *fakeAuthClient) DisableTOTP(_ context.Context, r *connect.Request[authv1.DisableTOTPRequest]) (*connect.Response[authv1.DisableTOTPResponse], error) {
	f.lastDisableReq = r.Msg
	if f.disableErr != nil {
		return nil, f.disableErr
	}
	if f.disableResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.disableResp), nil
}

func (f *fakeAuthClient) RegenerateRecoveryCodes(_ context.Context, r *connect.Request[authv1.RegenerateRecoveryCodesRequest]) (*connect.Response[authv1.RegenerateRecoveryCodesResponse], error) {
	f.lastRegenerateReq = r.Msg
	if f.regenerateErr != nil {
		return nil, f.regenerateErr
	}
	if f.regenerateResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.regenerateResp), nil
}

// Story 2.5 — GetMe + UpdateProfile stubs.
func (f *fakeAuthClient) GetMe(_ context.Context, r *connect.Request[authv1.GetMeRequest]) (*connect.Response[authv1.GetMeResponse], error) {
	f.lastGetMeReq = r.Msg
	if f.getMeErr != nil {
		return nil, f.getMeErr
	}
	if f.getMeResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.getMeResp), nil
}

func (f *fakeAuthClient) UpdateProfile(_ context.Context, r *connect.Request[authv1.UpdateProfileRequest]) (*connect.Response[authv1.UpdateProfileResponse], error) {
	f.lastUpdateProfileReq = r.Msg
	if f.updateProfileErr != nil {
		return nil, f.updateProfileErr
	}
	if f.updateProfileResp == nil {
		return nil, errFakeUnimplemented
	}
	return connect.NewResponse(f.updateProfileResp), nil
}

// Story 3.2 — ValidateApiKey is not exercised by api-gateway handler tests
// (the bearer-auth middleware lives in apps/api-gateway/internal/middleware
// and its tests use a stubAuthSvc instance). This stub keeps the interface
// satisfied so the existing 2FA / profile handler tests still compile.
func (f *fakeAuthClient) ValidateApiKey(_ context.Context, _ *connect.Request[authv1.ValidateApiKeyRequest]) (*connect.Response[authv1.ValidateApiKeyResponse], error) {
	return nil, errFakeUnimplemented
}

// Story 5.1 — CreateApiKey / ListApiKeys / RevokeApiKey stubs. Exercised
// directly by me_keys_test.go via a per-test specialised fake; this stub
// keeps the existing 2FA / profile handler tests compiling without forcing
// every test to wire the management surface.
func (f *fakeAuthClient) CreateApiKey(_ context.Context, _ *connect.Request[authv1.CreateApiKeyRequest]) (*connect.Response[authv1.CreateApiKeyResponse], error) {
	return nil, errFakeUnimplemented
}

func (f *fakeAuthClient) ListApiKeys(_ context.Context, _ *connect.Request[authv1.ListApiKeysRequest]) (*connect.Response[authv1.ListApiKeysResponse], error) {
	return nil, errFakeUnimplemented
}

func (f *fakeAuthClient) RevokeApiKey(_ context.Context, _ *connect.Request[authv1.RevokeApiKeyRequest]) (*connect.Response[authv1.RevokeApiKeyResponse], error) {
	return nil, errFakeUnimplemented
}

func (f *fakeAuthClient) UpdateApiKey(_ context.Context, _ *connect.Request[authv1.UpdateApiKeyRequest]) (*connect.Response[authv1.UpdateApiKeyResponse], error) {
	return nil, errFakeUnimplemented
}

// Story 5.4 — the gateway never calls GetCapNotificationContext (it's a
// notification-svc → auth-svc internal hop), but the test double must satisfy
// the extended AuthServiceClient interface.
func (f *fakeAuthClient) GetCapNotificationContext(_ context.Context, _ *connect.Request[authv1.GetCapNotificationContextRequest]) (*connect.Response[authv1.GetCapNotificationContextResponse], error) {
	return nil, errFakeUnimplemented
}

var _ authv1connect.AuthServiceClient = (*fakeAuthClient)(nil)

// --- helpers -------------------------------------------------------------

func postJSON(t *testing.T, h http.HandlerFunc, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	return rr
}

func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(rr.Body)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, raw)
	}
	return got
}

// --- tests ---------------------------------------------------------------

// Scenario: 2.2-INT-006
// Happy-path proxy: body is JSON {email, password, locale}; on auth-svc 200
// the gateway emits {message:"verification_email_sent"} with NO user_id
// leakage (anti-enumeration anchor).
func TestSignup_HappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	rr := postJSON(t, p.Signup, map[string]string{
		"email":    "user@example.com",
		"password": "correct horse battery staple",
		"locale":   "en",
	}, map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"User-Agent":      "Go-http-client/1.1",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeEnvelope(t, rr)
	if got["message"] != "verification_email_sent" {
		t.Errorf("body.message = %v, want verification_email_sent", got["message"])
	}
	// UNIT-044 anchor: NO user_id leak in the response body.
	if _, ok := got["user_id"]; ok {
		t.Errorf("body unexpectedly contains user_id: %v", got)
	}
	// Headers forwarded into the upstream Connect request.
	if fake.lastReq.GetClientIp() != "1.2.3.4" {
		t.Errorf("upstream client_ip = %q, want 1.2.3.4", fake.lastReq.GetClientIp())
	}
	if fake.lastReq.GetUserAgent() != "Go-http-client/1.1" {
		t.Errorf("upstream user_agent = %q, want Go-http-client/1.1", fake.lastReq.GetUserAgent())
	}
	if fake.lastReq.GetEmail() != "user@example.com" {
		t.Errorf("upstream email = %q", fake.lastReq.GetEmail())
	}
}

// Scenario: 2.2-INT-006 (X-Forwarded-For multi-hop).
// Comma-separated XFF — gateway takes the leftmost (the actual client).
func TestSignup_XForwardedForMultiHop(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	postJSON(t, p.Signup, map[string]string{
		"email": "u@e.com", "password": "0123456789", "locale": "en",
	}, map[string]string{"X-Forwarded-For": "203.0.113.5, 10.0.0.1, 10.0.0.2"})

	if fake.lastReq.GetClientIp() != "203.0.113.5" {
		t.Errorf("multi-hop XFF: client_ip = %q, want 203.0.113.5 (leftmost)", fake.lastReq.GetClientIp())
	}
}

// Malformed JSON body → 400 with the OpenAI envelope shape.
func TestSignup_MalformedJSONBody(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", strings.NewReader("not json"))
	rr := httptest.NewRecorder()
	p.Signup(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	errObj, _ := got["error"].(map[string]any)
	if errObj["code"] != "400_invalid_email" {
		t.Errorf("error.code = %v, want 400_invalid_email", errObj["code"])
	}
}

// Connect InvalidArgument with 400_invalid_email message → HTTP 400 envelope.
func TestSignup_TranslatesInvalidArgument(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		registerErr: connect.NewError(connect.CodeInvalidArgument, errors.New("400_invalid_email")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	errObj, _ := got["error"].(map[string]any)
	if errObj["code"] != "400_invalid_email" {
		t.Errorf("error.code = %v, want 400_invalid_email", errObj["code"])
	}
}

// Scenario: 2.2-INT-007 anchor — auth-svc 429_rate_limit_signup with
// Retry-After Connect metadata → HTTP 429 + Retry-After header.
func TestSignup_429RateLimitEmitsRetryAfter(t *testing.T) {
	t.Parallel()
	connectErr := connect.NewError(connect.CodeResourceExhausted, errors.New("429_rate_limit_signup"))
	connectErr.Meta().Set("Retry-After", "180")

	fake := &fakeAuthClient{registerErr: connectErr}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "180" {
		t.Errorf("Retry-After header = %q, want 180", got)
	}
	got := decodeEnvelope(t, rr)
	errObj, _ := got["error"].(map[string]any)
	if errObj["code"] != "429_rate_limit_signup" {
		t.Errorf("error.code = %v, want 429_rate_limit_signup", errObj["code"])
	}
}

// HIBP unavailable → HTTP 503.
func TestSignup_HIBPUnavailableMapsTo503(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		registerErr: connect.NewError(connect.CodeUnavailable, errors.New("503_hibp_unavailable")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "503_hibp_unavailable" {
		t.Errorf("error.code = %v, want 503_hibp_unavailable", got["error"])
	}
}

// Email-send failure → HTTP 500.
func TestSignup_EmailSendFailureMapsTo500(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		registerErr: connect.NewError(connect.CodeInternal, errors.New("500_email_send_failed")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "500_email_send_failed" {
		t.Errorf("error.code = %v", got["error"])
	}
}

// Connect error WITHOUT a canonical status code prefix → defensive fallback
// to 502_auth_svc_unavailable. This catches accidental error message drift
// in auth-svc.
func TestSignup_UnknownErrorShapeFallsBackTo502(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		registerErr: connect.NewError(connect.CodeUnknown, errors.New("something exploded")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)
	// CodeUnknown falls back via httpStatusForCode → 500; but the status
	// code in body is 502_auth_svc_unavailable (the defensive fallback).
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 500 or 502 fallback", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "502_auth_svc_unavailable" {
		t.Errorf("error.code = %v, want 502_auth_svc_unavailable fallback", got["error"])
	}
}

// "NNN_code: detail" form — gateway extracts the prefix correctly.
func TestSignup_ErrorMessageWithColonDetail(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		registerErr: connect.NewError(connect.CodeInvalidArgument, errors.New("400_password_breached: HIBP returned positive")),
	}
	p := handlers.NewAuthProxy(fake)
	rr := postJSON(t, p.Signup, map[string]string{
		"email": "ok@example.com", "password": "0123456789", "locale": "en",
	}, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "400_password_breached" {
		t.Errorf("error.code = %v, want 400_password_breached", got["error"])
	}
}

// As of P4d, no /v1/auth/* endpoints remain as 501 stubs — Signin +
// Refresh shipped real handlers. The TestStubHandlers_Return501 test
// from earlier phases is removed; future stubs (e.g., the Story 2.5+
// protected-route middleware) would add their own dedicated tests.

// --- VerifyEmail tests (P3c) ---------------------------------------------

// VerifyEmail happy path: token from query string → upstream call →
// response body carries {user_id, email_verified_at, status}.
func TestVerifyEmail_HappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/verify-email?token=ABCDEF", nil)
	req.Header.Set("X-Forwarded-For", "5.6.7.8")
	rr := httptest.NewRecorder()
	p.VerifyEmail(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeEnvelope(t, rr)
	if got["status"] != "email_verified" {
		t.Errorf("body.status = %v, want email_verified", got["status"])
	}
	if got["user_id"] == nil || got["user_id"] == "" {
		t.Errorf("body.user_id missing")
	}
	if got["email_verified_at"] == nil || got["email_verified_at"] == "" {
		t.Errorf("body.email_verified_at missing")
	}
	if fake.lastVerifyReq.GetToken() != "ABCDEF" {
		t.Errorf("upstream token = %q, want ABCDEF", fake.lastVerifyReq.GetToken())
	}
	if fake.lastVerifyReq.GetClientIp() != "5.6.7.8" {
		t.Errorf("upstream client_ip = %q, want 5.6.7.8", fake.lastVerifyReq.GetClientIp())
	}
}

// VerifyEmail without ?token=... query → 400_invalid_token, no upstream call.
func TestVerifyEmail_MissingTokenQuery(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/verify-email", nil)
	rr := httptest.NewRecorder()
	p.VerifyEmail(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "400_invalid_token" {
		t.Errorf("error.code = %v, want 400_invalid_token", got["error"])
	}
	if fake.lastVerifyReq != nil {
		t.Errorf("upstream called despite missing token; should short-circuit")
	}
}

// auth-svc 410_token_expired → gateway HTTP 410 with envelope.
func TestVerifyEmail_410TokenExpired(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		verifyErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("410_token_expired")),
	}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/verify-email?token=ABCDEF", nil)
	rr := httptest.NewRecorder()
	p.VerifyEmail(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "410_token_expired" {
		t.Errorf("error.code = %v, want 410_token_expired", got["error"])
	}
}

// auth-svc 410_token_used (brute-force lockout) → gateway HTTP 410.
func TestVerifyEmail_410TokenUsedBruteForce(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{
		verifyErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("410_token_used")),
	}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodGet, "/v1/auth/verify-email?token=ABCDEF", nil)
	rr := httptest.NewRecorder()
	p.VerifyEmail(rr, req)

	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "410_token_used" {
		t.Errorf("error.code = %v, want 410_token_used", got["error"])
	}
}

// --- ResendVerification tests (P3c) --------------------------------------

// Happy path: 200 + {status:"ok"} regardless of which auth-svc branch ran
// (caller cannot tell from the response).
func TestResendVerification_HappyPath(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	rr := postJSON(t, p.ResendVerification, map[string]string{"email": "user@example.com"}, map[string]string{
		"X-Forwarded-For": "9.9.9.9",
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	got := decodeEnvelope(t, rr)
	if got["status"] != "ok" {
		t.Errorf("body.status = %v, want ok", got["status"])
	}
	if fake.lastResendReq.GetEmail() != "user@example.com" {
		t.Errorf("upstream email = %q, want user@example.com", fake.lastResendReq.GetEmail())
	}
	if fake.lastResendReq.GetClientIp() != "9.9.9.9" {
		t.Errorf("upstream client_ip = %q, want 9.9.9.9", fake.lastResendReq.GetClientIp())
	}
}

// Malformed JSON body → 400_invalid_email, no upstream call.
func TestResendVerification_MalformedJSON(t *testing.T) {
	t.Parallel()
	fake := &fakeAuthClient{}
	p := handlers.NewAuthProxy(fake)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/resend-verification", strings.NewReader("not json"))
	rr := httptest.NewRecorder()
	p.ResendVerification(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "400_invalid_email" {
		t.Errorf("error.code = %v, want 400_invalid_email", got["error"])
	}
	if fake.lastResendReq != nil {
		t.Errorf("upstream called despite malformed body")
	}
}

// auth-svc 429_rate_limit_resend_ip with Retry-After Connect metadata →
// HTTP 429 + Retry-After response header.
func TestResendVerification_429RateLimitEmitsRetryAfter(t *testing.T) {
	t.Parallel()
	connectErr := connect.NewError(connect.CodeResourceExhausted, errors.New("429_rate_limit_resend_ip"))
	connectErr.Meta().Set("Retry-After", "300")
	fake := &fakeAuthClient{resendErr: connectErr}
	p := handlers.NewAuthProxy(fake)

	rr := postJSON(t, p.ResendVerification, map[string]string{"email": "user@example.com"}, nil)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "300" {
		t.Errorf("Retry-After header = %q, want 300", got)
	}
	got := decodeEnvelope(t, rr)
	if got["error"].(map[string]any)["code"] != "429_rate_limit_resend_ip" {
		t.Errorf("error.code = %v, want 429_rate_limit_resend_ip", got["error"])
	}
}
