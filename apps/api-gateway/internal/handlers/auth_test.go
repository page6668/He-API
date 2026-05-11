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
	// captured inputs for assertions
	lastReq *authv1.RegisterUserRequest
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
func (f *fakeAuthClient) VerifyEmail(_ context.Context, _ *connect.Request[authv1.VerifyEmailRequest]) (*connect.Response[authv1.VerifyEmailResponse], error) {
	return nil, errFakeUnimplemented
}
func (f *fakeAuthClient) ResendVerification(_ context.Context, _ *connect.Request[authv1.ResendVerificationRequest]) (*connect.Response[authv1.ResendVerificationResponse], error) {
	return nil, errFakeUnimplemented
}
func (f *fakeAuthClient) LoginUser(_ context.Context, _ *connect.Request[authv1.LoginUserRequest]) (*connect.Response[authv1.LoginUserResponse], error) {
	return nil, errFakeUnimplemented
}
func (f *fakeAuthClient) RefreshToken(_ context.Context, _ *connect.Request[authv1.RefreshTokenRequest]) (*connect.Response[authv1.RefreshTokenResponse], error) {
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

// Stubs for the four other endpoints still return 501.
func TestStubHandlers_Return501(t *testing.T) {
	t.Parallel()
	p := handlers.NewAuthProxy(&fakeAuthClient{})
	for _, h := range []http.HandlerFunc{p.VerifyEmail, p.ResendVerification, p.Signin, p.Refresh} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(""))
		h(rr, req)
		if rr.Code != http.StatusNotImplemented {
			t.Errorf("stub returned %d, want 501", rr.Code)
		}
		got := decodeEnvelope(t, rr)
		if got["error"].(map[string]any)["code"] != "501_not_implemented" {
			t.Errorf("stub error.code = %v, want 501_not_implemented", got["error"])
		}
	}
}
