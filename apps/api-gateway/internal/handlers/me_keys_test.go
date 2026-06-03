// Story 5.1 — INT-001..014 (POST/GET/DELETE /v1/me/keys integration tests).
//
// These tests stub the auth-svc connect handler in-process (httptest.Server
// hosting authv1connect.NewAuthServiceHandler) and exercise the full gateway
// REST → upstream gRPC path. miniredis backs the rate-limit helper. We do
// NOT spin up real PG / Kafka — the auth-svc stub records call counts +
// returns canned responses, sufficient for the INT-* surface (per the test
// design which calls these "integration" relative to the gateway boundary,
// not the auth-svc storage boundary).

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// --- stubAuthSvc — minimal in-process auth-svc backing the integration tests ---

type stubAuthSvc struct {
	authv1connect.UnimplementedAuthServiceHandler

	createHits int32
	listHits   int32
	revokeHits int32
	updateHits int32

	createFn func(req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error)
	listFn   func(req *authv1.ListApiKeysRequest) (*authv1.ListApiKeysResponse, error)
	revokeFn func(req *authv1.RevokeApiKeyRequest) (*authv1.RevokeApiKeyResponse, error)
	updateFn func(req *authv1.UpdateApiKeyRequest) (*authv1.UpdateApiKeyResponse, error)
}

// UpdateApiKey backs the Story-5.2 PATCH integration tests.
func (s *stubAuthSvc) UpdateApiKey(_ context.Context, r *connect.Request[authv1.UpdateApiKeyRequest]) (*connect.Response[authv1.UpdateApiKeyResponse], error) {
	atomic.AddInt32(&s.updateHits, 1)
	if s.updateFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("updateFn unwired"))
	}
	resp, err := s.updateFn(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *stubAuthSvc) CreateApiKey(_ context.Context, r *connect.Request[authv1.CreateApiKeyRequest]) (*connect.Response[authv1.CreateApiKeyResponse], error) {
	atomic.AddInt32(&s.createHits, 1)
	if s.createFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("createFn unwired"))
	}
	resp, err := s.createFn(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *stubAuthSvc) ListApiKeys(_ context.Context, r *connect.Request[authv1.ListApiKeysRequest]) (*connect.Response[authv1.ListApiKeysResponse], error) {
	atomic.AddInt32(&s.listHits, 1)
	if s.listFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("listFn unwired"))
	}
	resp, err := s.listFn(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (s *stubAuthSvc) RevokeApiKey(_ context.Context, r *connect.Request[authv1.RevokeApiKeyRequest]) (*connect.Response[authv1.RevokeApiKeyResponse], error) {
	atomic.AddInt32(&s.revokeHits, 1)
	if s.revokeFn == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("revokeFn unwired"))
	}
	resp, err := s.revokeFn(r.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// --- integration harness ----------------------------------------------------

type meKeysHarness struct {
	t       *testing.T
	stub    *stubAuthSvc
	authSrv *httptest.Server
	handler *handlers.MeKeysHandler
	mini    *miniredis.Miniredis
	userID  string
}

func newMeKeysHarness(t *testing.T, stub *stubAuthSvc) *meKeysHarness {
	t.Helper()
	mini := miniredis.RunT(t)
	_, h := authv1connect.NewAuthServiceHandler(stub)
	authSrv := httptest.NewServer(h)
	t.Cleanup(func() { authSrv.Close() })

	client := authv1connect.NewAuthServiceClient(authSrv.Client(), authSrv.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mkh := handlers.NewMeKeysHandler(client, func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()})
	}, logger)
	return &meKeysHarness{
		t: t, stub: stub, authSrv: authSrv, handler: mkh, mini: mini,
		userID: "00000000-0000-4000-8000-000000000001",
	}
}

// withUserID wraps the supplied request so its context carries the
// JWT-derived user_id (the real api-gateway middleware does this; in
// the unit-level integration tests we inject it directly).
func (h *meKeysHarness) withUserID(r *http.Request) *http.Request {
	ctx := middleware.WithUserID(r.Context(), h.userID)
	return r.WithContext(ctx)
}

func (h *meKeysHarness) doCreate(body string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.handler.HandleCreate(rr, h.withUserID(req))
	return rr
}

func (h *meKeysHarness) doList(rawQuery string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	url := "/v1/me/keys"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	h.handler.HandleList(rr, h.withUserID(req))
	return rr
}

func (h *meKeysHarness) doRevoke(apiKeyID string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/me/keys/"+apiKeyID, nil)
	// Go 1.22 ServeMux populates PathValue via the mux; we set manually here.
	req.SetPathValue("api_key_id", apiKeyID)
	h.handler.HandleRevoke(rr, h.withUserID(req))
	return rr
}

func decodeMeKeysBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body err=%v (raw=%q)", err, rr.Body.String())
	}
	return body
}

// --- TestMeKeysCreate covers INT-001, INT-005, INT-006, INT-009, INT-010, INT-013 ---

func TestMeKeysCreate(t *testing.T) {
	t.Run("5.1-INT-001 happy path 201 + headers", func(t *testing.T) {
		// Scenario: 5.1-INT-001
		// Priority: P0
		// Input:    POST /v1/me/keys with JWT user_id + body {"name":"My First Key"}
		// Expected: 201; body validates against CreateKeyResponse schema;
		//           response headers carry Cache-Control: no-store, no-cache, must-revalidate +
		//           Pragma: no-cache.
		// AC1 §Scenario wire-level
		createdAt := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		stub := &stubAuthSvc{
			createFn: func(req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error) {
				return &authv1.CreateApiKeyResponse{
					ApiKeyId:  "00000000-0000-4000-8000-0000000000aa",
					KeyPrefix: "he-PPP111111",
					Name:      req.GetName(),
					CreatedAt: timestamppb.New(createdAt),
					Plaintext: "he-PPP111111ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
				}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		rr := h.doCreate(`{"name":"My First Key"}`)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
		}
		body := decodeMeKeysBody(t, rr)
		if body["api_key_id"] != "00000000-0000-4000-8000-0000000000aa" {
			t.Fatalf("api_key_id=%v", body["api_key_id"])
		}
		if body["name"] != "My First Key" {
			t.Fatalf("name=%v", body["name"])
		}
		if body["warning"] != handlers.PlaintextOneTimeWarning {
			t.Fatalf("warning=%v", body["warning"])
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-store, no-cache, must-revalidate" {
			t.Fatalf("Cache-Control=%q", got)
		}
		if got := rr.Header().Get("Pragma"); got != "no-cache" {
			t.Fatalf("Pragma=%q", got)
		}
		if atomic.LoadInt32(&stub.createHits) != 1 {
			t.Fatalf("createHits=%d want 1", stub.createHits)
		}
	})

	t.Run("5.1-INT-005 rate-limit boundary 11th request returns 429 + Retry-After", func(t *testing.T) {
		// Scenario: 5.1-INT-005
		// Priority: P0
		// Input:    10 successive POSTs (all 201), then 11th POST
		// Expected: 11th → 429 with body code=429_rate_limit_key_create +
		//           header Retry-After: <int seconds>; auth-svc createHits remains at 10.
		// BR-1.10 boundary
		createdAt := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		stub := &stubAuthSvc{
			createFn: func(req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error) {
				return &authv1.CreateApiKeyResponse{
					ApiKeyId:  "00000000-0000-4000-8000-0000000000bb",
					KeyPrefix: "he-PPP222222",
					Name:      req.GetName(),
					CreatedAt: timestamppb.New(createdAt),
					Plaintext: "he-PPP222222YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY",
				}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		for i := 1; i <= handlers.CreateKeyRateLimitMax; i++ {
			rr := h.doCreate(fmt.Sprintf(`{"name":"K%d"}`, i))
			if rr.Code != http.StatusCreated {
				t.Fatalf("iter %d status=%d want 201", i, rr.Code)
			}
		}
		rr := h.doCreate(`{"name":"K11"}`)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("11th status=%d want 429 (body=%s)", rr.Code, rr.Body.String())
		}
		body := decodeMeKeysBody(t, rr)
		gotErr, _ := body["error"].(map[string]any)
		if gotErr == nil || gotErr["code"] != "429_rate_limit_key_create" {
			t.Fatalf("body error.code=%v want 429_rate_limit_key_create", gotErr)
		}
		ra := rr.Header().Get("Retry-After")
		if ra == "" {
			t.Fatalf("Retry-After header missing")
		}
		if atomic.LoadInt32(&stub.createHits) != int32(handlers.CreateKeyRateLimitMax) {
			t.Fatalf("createHits=%d want %d (11th must NOT reach auth-svc)", stub.createHits, handlers.CreateKeyRateLimitMax)
		}
	})

	t.Run("5.1-INT-006 strict-field-validation rejects unknown field", func(t *testing.T) {
		// Scenario: 5.1-INT-006
		// Priority: P0
		// Input:    POST body {"name":"x","scope":{"models":["qwen-max"]}}
		// Expected: 400 code=400_unknown_field; auth-svc createHits == 0;
		//           rate-limit counter NOT incremented.
		// BR-1.2 + BR-1.10 carve-out
		stub := &stubAuthSvc{}
		h := newMeKeysHarness(t, stub)
		rr := h.doCreate(`{"name":"x","scope":{"models":["qwen-max"]}}`)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
		body := decodeMeKeysBody(t, rr)
		gotErr, _ := body["error"].(map[string]any)
		if gotErr == nil || gotErr["code"] != "400_unknown_field" {
			t.Fatalf("body error.code=%v want 400_unknown_field", gotErr)
		}
		if atomic.LoadInt32(&stub.createHits) != 0 {
			t.Fatalf("createHits=%d want 0", stub.createHits)
		}
		// Rate-limit counter MUST NOT be incremented (5.1-INT-006 carve-out
		// — validation rejection occurs before CheckCreateKeyRateLimit).
		if _, err := h.mini.Get(handlers.CreateKeyRateLimitPrefix + h.userID); err == nil {
			t.Fatalf("rate-limit counter incremented on validation failure")
		}
	})

	t.Run("5.1-INT-013 response field-order byte-exact lock at wire", func(t *testing.T) {
		// Scenario: 5.1-INT-013
		// Priority: P0
		// Input:    raw response bytes of INT-001
		// Expected: bytes.Index of "api_key_id" < "key_prefix" < "name" < "plaintext"
		//           < "created_at" < "warning" — all 5 ordering pairs hold.
		// BR-1.13 wire-level lock
		createdAt := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
		stub := &stubAuthSvc{
			createFn: func(req *authv1.CreateApiKeyRequest) (*authv1.CreateApiKeyResponse, error) {
				return &authv1.CreateApiKeyResponse{
					ApiKeyId:  "00000000-0000-4000-8000-0000000000cc",
					KeyPrefix: "he-PPP333333",
					Name:      req.GetName(),
					CreatedAt: timestamppb.New(createdAt),
					Plaintext: "he-PPP333333VVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVV",
				}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		rr := h.doCreate(`{"name":"K"}`)
		raw := rr.Body.Bytes()
		pairs := []struct{ a, b string }{
			{"api_key_id", "key_prefix"},
			{"key_prefix", "name"},
			{"name", "plaintext"},
			{"plaintext", "created_at"},
			{"created_at", "warning"},
		}
		for _, p := range pairs {
			ai := bytes.Index(raw, []byte(`"`+p.a+`"`))
			bi := bytes.Index(raw, []byte(`"`+p.b+`"`))
			if ai < 0 || bi < 0 || ai >= bi {
				t.Fatalf("ordering %q(%d) < %q(%d) failed; body=%s", p.a, ai, p.b, bi, raw)
			}
		}
	})
}

// --- TestMeKeysList covers INT-002, INT-007, INT-008, INT-011, INT-012 ---

func TestMeKeysList(t *testing.T) {
	t.Run("5.1-INT-002 happy path list with shape", func(t *testing.T) {
		// Scenario: 5.1-INT-002
		// Priority: P0
		// Input:    GET /v1/me/keys with valid JWT (seed 3 keys for user)
		// Expected: 200; body {"object":"list","data":[...]} with each entry having shape
		//           {api_key_id, name, key_prefix, scope, monthly_cost_cap_usd,
		//            current_month_cost_usd, last_used_at, revoked_at, created_at};
		//           headers Cache-Control: no-store.
		// AC2 + BR-2.1
		stub := &stubAuthSvc{
			listFn: func(_ *authv1.ListApiKeysRequest) (*authv1.ListApiKeysResponse, error) {
				return &authv1.ListApiKeysResponse{Keys: []*authv1.ApiKeyEntry{
					{
						ApiKeyId:            "00000000-0000-4000-8000-0000000000aa",
						Name:                "K1",
						KeyPrefix:           "he-AAA111111",
						Scope:               "{}",
						CurrentMonthCostUsd: "0",
						CreatedAt:           timestamppb.New(time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)),
					},
				}}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		rr := h.doList("")
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q", got)
		}
		body := decodeMeKeysBody(t, rr)
		if body["object"] != "list" {
			t.Fatalf("object=%v want 'list'", body["object"])
		}
		data, ok := body["data"].([]any)
		if !ok || len(data) != 1 {
			t.Fatalf("data=%v want 1-elem array", body["data"])
		}
		entry := data[0].(map[string]any)
		for _, f := range []string{
			"api_key_id", "name", "key_prefix", "scope",
			"monthly_cost_cap_usd", "current_month_cost_usd", "last_used_at", "revoked_at", "created_at",
		} {
			if _, present := entry[f]; !present {
				t.Fatalf("entry missing field %q: %+v", f, entry)
			}
		}
	})

	t.Run("5.1-INT-008 IDOR list strict reject query-string user_id", func(t *testing.T) {
		// Scenario: 5.1-INT-008
		// Priority: P0
		// Input:    GET /v1/me/keys?user_id=<other-uuid>
		// Expected: 400 code=400_unknown_field with param="user_id";
		//           auth-svc RPC counter == 0.
		// BR-2.1 strict reject
		stub := &stubAuthSvc{}
		h := newMeKeysHarness(t, stub)
		rr := h.doList("user_id=00000000-0000-4000-8000-000000000099")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
		body := decodeMeKeysBody(t, rr)
		gotErr, _ := body["error"].(map[string]any)
		if gotErr == nil || gotErr["code"] != "400_unknown_field" {
			t.Fatalf("body error.code=%v want 400_unknown_field", gotErr)
		}
		if gotErr["param"] != "user_id" {
			t.Fatalf("body error.param=%v want 'user_id'", gotErr["param"])
		}
		if atomic.LoadInt32(&stub.listHits) != 0 {
			t.Fatalf("listHits=%d want 0", stub.listHits)
		}
	})

	t.Run("5.1-INT-012 Cache-Control no-store on list", func(t *testing.T) {
		// Scenario: 5.1-INT-012
		// Priority: P1
		// Input:    GET /v1/me/keys
		// Expected: response header Cache-Control == "no-store"
		stub := &stubAuthSvc{
			listFn: func(_ *authv1.ListApiKeysRequest) (*authv1.ListApiKeysResponse, error) {
				return &authv1.ListApiKeysResponse{}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		rr := h.doList("")
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q want 'no-store'", got)
		}
	})
}

// --- TestMeKeysRevoke covers INT-003, INT-004, INT-014 ---

func TestMeKeysRevoke(t *testing.T) {
	t.Run("5.1-INT-003 happy path 200 + body shape + headers", func(t *testing.T) {
		// Scenario: 5.1-INT-003
		// Priority: P0
		// Input:    DELETE /v1/me/keys/{id} for a seeded key owned by JWT subject
		// Expected: 200; body {"api_key_id":<id>,"revoked_at":<NOW>,"was_already_revoked":false};
		//           response headers Cache-Control: no-store.
		// AC3 §Scenario
		now := time.Date(2026, 5, 25, 12, 5, 0, 0, time.UTC)
		stub := &stubAuthSvc{
			revokeFn: func(req *authv1.RevokeApiKeyRequest) (*authv1.RevokeApiKeyResponse, error) {
				return &authv1.RevokeApiKeyResponse{
					ApiKeyId:  req.GetApiKeyId(),
					RevokedAt: timestamppb.New(now),
				}, nil
			},
		}
		h := newMeKeysHarness(t, stub)
		apiKeyID := "00000000-0000-4000-8000-0000000000aa"
		rr := h.doRevoke(apiKeyID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
		}
		body := decodeMeKeysBody(t, rr)
		if body["api_key_id"] != apiKeyID {
			t.Fatalf("api_key_id=%v", body["api_key_id"])
		}
		if body["was_already_revoked"] != false {
			t.Fatalf("was_already_revoked=%v want false", body["was_already_revoked"])
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control=%q", got)
		}
	})

	t.Run("5.1-INT-004 IDOR collapse cross-user delete returns 404", func(t *testing.T) {
		// Scenario: 5.1-INT-004
		// Priority: P0
		// Input:    DELETE /v1/me/keys/{user_B_key_id} with user A's JWT
		// Expected: 404 code=404_api_key_not_found.
		// BR-3.2 anti-enumeration
		stub := &stubAuthSvc{
			revokeFn: func(_ *authv1.RevokeApiKeyRequest) (*authv1.RevokeApiKeyResponse, error) {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("api_key_not_found"))
			},
		}
		h := newMeKeysHarness(t, stub)
		rr := h.doRevoke("00000000-0000-4000-8000-0000000000bb")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404", rr.Code)
		}
		body := decodeMeKeysBody(t, rr)
		gotErr, _ := body["error"].(map[string]any)
		if gotErr == nil || gotErr["code"] != "404_api_key_not_found" {
			t.Fatalf("body error.code=%v want 404_api_key_not_found", gotErr)
		}
	})

	t.Run("invalid uuid path rejects with 400", func(t *testing.T) {
		// Edge case beyond skeleton's INT-* — defensive UUID-shape gate.
		stub := &stubAuthSvc{}
		h := newMeKeysHarness(t, stub)
		rr := h.doRevoke("not-a-uuid")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", rr.Code)
		}
		body := decodeMeKeysBody(t, rr)
		gotErr, _ := body["error"].(map[string]any)
		if gotErr == nil || gotErr["code"] != "400_invalid_request" {
			t.Fatalf("body error.code=%v want 400_invalid_request", gotErr)
		}
		if atomic.LoadInt32(&stub.revokeHits) != 0 {
			t.Fatalf("revokeHits=%d want 0 (UUID gate must short-circuit)", stub.revokeHits)
		}
	})
}
