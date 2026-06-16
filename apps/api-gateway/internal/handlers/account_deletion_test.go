package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func deletionReq(t *testing.T, method, target, body, userID string) *http.Request {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != "" {
		r = r.WithContext(middleware.WithUserID(r.Context(), userID))
	}
	return r
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return env.Error.Code
}

// AC2 happy path: 200 with status + pending_deletion_at + can_cancel_until.
func TestGW_RequestDeletion_HappyPath(t *testing.T) {
	pendingAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	fake := &fakeAuthClient{reqDeletionResp: &authv1.RequestAccountDeletionResponse{
		Status:            "pending_deletion",
		PendingDeletionAt: timestamppb.New(pendingAt),
		CanCancelUntil:    timestamppb.New(pendingAt),
	}}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.RequestDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion", `{"reauth":{"password":"pw"}}`, "u-1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "pending_deletion" || body["pending_deletion_at"] != "2026-07-16T09:00:00.000Z" {
		t.Errorf("body = %v", body)
	}
	if fake.lastReqDeletionReq.GetUserId() != "u-1" || fake.lastReqDeletionReq.GetReauth().GetPassword() != "pw" {
		t.Errorf("upstream req = %+v", fake.lastReqDeletionReq)
	}
}

// SEC-002 / UNIT-015 — body carrying user_id → 400_invalid_body (IDOR defence);
// upstream MUST NOT be called.
func TestGW_RequestDeletion_RejectsBodyUserID(t *testing.T) {
	fake := &fakeAuthClient{}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.RequestDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion", `{"user_id":"victim","reauth":{"password":"pw"}}`, "u-1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if got := errCode(t, rec); got != "400_invalid_body" {
		t.Errorf("code = %q, want 400_invalid_body", got)
	}
	if fake.lastReqDeletionReq != nil {
		t.Errorf("upstream must not be called when body is rejected")
	}
}

// 403_bad_reauth passthrough — auth-svc encodes the code in the connect message.
func TestGW_RequestDeletion_BadReauthPassthrough(t *testing.T) {
	fake := &fakeAuthClient{reqDeletionErr: connect.NewError(connect.CodePermissionDenied, errors.New("403_bad_reauth"))}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.RequestDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion", `{"reauth":{"password":"x"}}`, "u-1"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	if got := errCode(t, rec); got != "403_bad_reauth" {
		t.Errorf("code = %q", got)
	}
}

// 409_account_not_deletable → HTTP 409 (validates the new httpStatusForCode case).
func TestGW_RequestDeletion_NotDeletable409(t *testing.T) {
	fake := &fakeAuthClient{reqDeletionErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("409_account_not_deletable"))}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.RequestDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion", `{}`, "u-1"))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if got := errCode(t, rec); got != "409_account_not_deletable" {
		t.Errorf("code = %q", got)
	}
}

// No JWT context → 401.
func TestGW_RequestDeletion_NoJWT(t *testing.T) {
	p := handlers.NewAccountDeletionProxy(&fakeAuthClient{})
	rec := httptest.NewRecorder()
	p.RequestDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion", `{}`, ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

// AC3 cancel happy path → 200 {active}.
func TestGW_CancelDeletion_HappyPath(t *testing.T) {
	fake := &fakeAuthClient{cancelDeletionResp: &authv1.CancelAccountDeletionResponse{Status: "active"}}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.CancelDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion/cancel", `{}`, "u-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "active" {
		t.Errorf("status = %q", body["status"])
	}
}

// AC3 cancel grace-expired → 410.
func TestGW_CancelDeletion_GraceExpired410(t *testing.T) {
	fake := &fakeAuthClient{cancelDeletionErr: connect.NewError(connect.CodeFailedPrecondition, errors.New("410_grace_expired"))}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.CancelDeletion(rec, deletionReq(t, "POST", "/v1/account/deletion/cancel", ``, "u-1"))
	if rec.Code != http.StatusGone {
		t.Fatalf("status %d, want 410", rec.Code)
	}
	if got := errCode(t, rec); got != "410_grace_expired" {
		t.Errorf("code = %q", got)
	}
}

// GET state hydration → has_password/totp_enabled/status/pending_deletion_at.
func TestGW_GetDeletionState(t *testing.T) {
	pendingAt := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	fake := &fakeAuthClient{deletionStateResp: &authv1.GetAccountDeletionStateResponse{
		Status:            "pending_deletion",
		PendingDeletionAt: timestamppb.New(pendingAt),
		HasPassword:       true,
		TotpEnabled:       false,
		Timezone:          "Asia/Shanghai",
	}}
	p := handlers.NewAccountDeletionProxy(fake)
	rec := httptest.NewRecorder()
	p.GetDeletionState(rec, deletionReq(t, "GET", "/v1/account/deletion", "", "u-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status            string  `json:"status"`
		PendingDeletionAt *string `json:"pending_deletion_at"`
		HasPassword       bool    `json:"has_password"`
		TotpEnabled       bool    `json:"totp_enabled"`
		Timezone          string  `json:"timezone"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.HasPassword || body.TotpEnabled || body.Status != "pending_deletion" || body.Timezone != "Asia/Shanghai" {
		t.Errorf("body = %+v", body)
	}
	if body.PendingDeletionAt == nil || *body.PendingDeletionAt != "2026-07-16T09:00:00.000Z" {
		t.Errorf("pending_deletion_at = %v", body.PendingDeletionAt)
	}
}

var _ = context.Background
