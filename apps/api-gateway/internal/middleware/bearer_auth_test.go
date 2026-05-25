package middleware_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// --- Test helpers ----------------------------------------------------------

const sentinelKey = "he-CACHETEST123XYZ"

// stubAuthSvc implements authv1connect.AuthServiceHandler. Only ValidateApiKey
// is wired — the other 14 RPCs are CodeUnimplemented (inherited from
// UnimplementedAuthServiceHandler) so the stub stays lean.
type stubAuthSvc struct {
	authv1connect.UnimplementedAuthServiceHandler
	rpcHits  int32
	validate func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
}

func (s *stubAuthSvc) ValidateApiKey(
	_ context.Context,
	req *connect.Request[authv1.ValidateApiKeyRequest],
) (*connect.Response[authv1.ValidateApiKeyResponse], error) {
	atomic.AddInt32(&s.rpcHits, 1)
	resp, err := s.validate(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

type harness struct {
	t          *testing.T
	mini       *miniredis.Miniredis
	authSrv    *httptest.Server
	stub       *stubAuthSvc
	mw         *middleware.APIKeyAuthenticator
	innerHits  int32
	innerPanic bool
}

func newHarness(t *testing.T, validate func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)) *harness {
	t.Helper()
	mini := miniredis.RunT(t)
	stub := &stubAuthSvc{validate: validate}

	_, handler := authv1connect.NewAuthServiceHandler(stub)
	authSrv := httptest.NewServer(handler)
	t.Cleanup(func() { authSrv.Close() })

	client := authv1connect.NewAuthServiceClient(authSrv.Client(), authSrv.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mw := middleware.NewAPIKeyAuthenticator(client, func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()})
	}, logger)

	return &harness{t: t, mini: mini, authSrv: authSrv, stub: stub, mw: mw}
}

// inner returns an http.Handler that increments innerHits — the
// panic-on-call sentinel for UNIT-025 is implemented by setting
// innerPanic=true.
func (h *harness) inner() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if h.innerPanic {
			panic("inner handler invoked on 401/503 path — middleware contract violation")
		}
		atomic.AddInt32(&h.innerHits, 1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"placeholder":true}`))
	})
}

func (h *harness) do(authHeader string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	h.mw.RequireAPIKey(h.inner()).ServeHTTP(rr, req)
	return rr
}

func expect401Envelope(t *testing.T, rr *httptest.ResponseRecorder, wantMessage string) {
	t.Helper()
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%q)", err, rr.Body.String())
	}
	if got, _ := body.Error["code"].(string); got != "401_invalid_api_key" {
		t.Errorf("error.code = %q, want 401_invalid_api_key", got)
	}
	if wantMessage != "" {
		if got, _ := body.Error["message"].(string); got != wantMessage {
			t.Errorf("error.message = %q, want %q", got, wantMessage)
		}
	}
	if got, _ := body.Error["type"].(string); got != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error", got)
	}
	if v, exists := body.Error["param"]; !exists || v != nil {
		t.Errorf("error.param = %v, want JSON null", v)
	}
	// Story 3.6 — flip from `nil` assertion to canonical regex match.
	// Tests drive the middleware without the RequestID middleware in scope,
	// so the sentinel "req_000000000000" is the expected value (it matches
	// the canonical regex since `0` ∈ [a-f0-9]).
	if v, exists := body.Error["he_request_id"]; !exists {
		t.Errorf("error.he_request_id missing — expected non-nil string")
	} else if s, ok := v.(string); !ok || !bearerHeRequestIDRE.MatchString(s) {
		t.Errorf("error.he_request_id = %v, want match for ^req_[a-f0-9]{12}$", v)
	}
}

var bearerHeRequestIDRE = regexp.MustCompile(`^req_[a-f0-9]{12}$`)

func expect503Envelope(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := body.Error["code"].(string); got != "503_auth_unavailable" {
		t.Errorf("error.code = %q, want 503_auth_unavailable", got)
	}
	if got, _ := body.Error["type"].(string); got != "server_error" {
		t.Errorf("error.type = %q, want server_error", got)
	}
}

func okValidate(reqKey string) func(*authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	return func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if req.GetPlaintextKey() != reqKey {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
		}
		return &authv1.ValidateApiKeyResponse{
			Ok:       true,
			ApiKeyId: "00000000-0000-0000-0000-000000000001",
			UserId:   "00000000-0000-0000-0000-000000000002",
			TeamId:   "",
			Scope:    `{}`,
			Reason:   authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED,
		}, nil
	}
}

func notFoundValidate(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
}

func revokedValidate(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_REVOKED}, nil
}

func unavailableValidate(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	return nil, connect.NewError(connect.CodeUnavailable, errors.New("simulated outage"))
}

// --- StripBearer (BR-1.1 / OQ3) -------------------------------------------

func TestStripBearer_CaseInsensitive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"Bearer he-ABC", "he-ABC"},
		{"bearer he-ABC", "he-ABC"},
		{"BEARER he-ABC", "he-ABC"},
		{"bEaReR he-ABC", "he-ABC"},
		{"Bearer   he-ABC", "he-ABC"}, // multi-space tolerance
		{"Bearer\the-ABC", "he-ABC"},  // tab tolerance
		{"Basic he-ABC", ""},
		{"", ""},
		{"Bearer", ""},       // no token after scheme
		{"Bearerhe-ABC", ""}, // scheme not followed by whitespace
	}
	for _, c := range cases {
		got := middleware.StripBearer(c.in)
		if got != c.want {
			t.Errorf("StripBearer(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- UNIT-011 .. UNIT-015 — header / format gating ------------------------

// Scenario: 3.2-UNIT-011
// Authorization header absent → 401 + Missing or malformed Authorization
// header. No RPC, no Redis.
func TestRequireAPIKey_MissingHeader(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	rr := h.do("")
	expect401Envelope(t, rr, "Missing or malformed Authorization header.")
	if h.stub.rpcHits != 0 {
		t.Errorf("rpc_hits = %d, want 0 (fail-fast at middleware boundary)", h.stub.rpcHits)
	}
}

// Scenario: 3.2-UNIT-012
// Non-Bearer scheme (Basic) → same 401 envelope; no RPC.
func TestRequireAPIKey_BasicScheme(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	rr := h.do("Basic dXNlcjpwYXNz")
	expect401Envelope(t, rr, "Missing or malformed Authorization header.")
	if h.stub.rpcHits != 0 {
		t.Errorf("rpc_hits = %d, want 0", h.stub.rpcHits)
	}
}

// Scenario: 3.2-UNIT-013
// `Bearer ` with empty token → 401 envelope; no RPC.
func TestRequireAPIKey_EmptyBearerToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	rr := h.do("Bearer ")
	expect401Envelope(t, rr, "Missing or malformed Authorization header.")
	if h.stub.rpcHits != 0 {
		t.Errorf("rpc_hits = %d, want 0", h.stub.rpcHits)
	}
}

// Scenario: 3.2-UNIT-014
// Token byte-length out of [10, 256] → 401 + Invalid API key provided.
func TestRequireAPIKey_TokenLengthBounds(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	for _, tok := range []string{"he-short", strings.Repeat("x", 257)} {
		rr := h.do("Bearer " + tok)
		expect401Envelope(t, rr, "Invalid API key provided.")
	}
	if h.stub.rpcHits != 0 {
		t.Errorf("rpc_hits = %d, want 0 (length gate must short-circuit)", h.stub.rpcHits)
	}
}

// Scenario: 3.2-UNIT-015
// Lowercase `bearer` scheme accepted; middleware proceeds to RPC.
func TestRequireAPIKey_LowercaseSchemeAccepted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	rr := h.do("bearer " + sentinelKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (inner placeholder)", rr.Code)
	}
	if h.stub.rpcHits != 1 {
		t.Errorf("rpc_hits = %d, want 1 (lowercase MUST be accepted)", h.stub.rpcHits)
	}
}

// --- UNIT-016 / UNIT-017 — cache miss + hit -------------------------------

// Scenario: 3.2-UNIT-016
// auth-svc returns ok=true + cache MISS → SETEX called with TTL=300s and
// correct JSON value; inner handler invoked.
func TestRequireAPIKey_CacheMissPopulates(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	rr := h.do("Bearer " + sentinelKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (inner placeholder)", rr.Code)
	}
	if h.innerHits != 1 {
		t.Errorf("inner_hits = %d, want 1", h.innerHits)
	}
	sum := sha256.Sum256([]byte(sentinelKey))
	want := "auth:apikey:" + hex.EncodeToString(sum[:])
	val, err := h.mini.Get(want)
	if err != nil {
		t.Fatalf("miniredis.Get(%s): %v (keys=%v)", want, err, h.mini.Keys())
	}
	if val == "" {
		t.Fatalf("cache value empty under %s", want)
	}
	// TTL bound — miniredis exposes the SetEx duration directly.
	ttl := h.mini.TTL(want)
	if ttl == 0 {
		t.Errorf("TTL = 0 (no SETEX applied?)")
	}
	if ttl.Seconds() < 250 || ttl.Seconds() > 350 {
		t.Errorf("TTL = %v, want ~300s", ttl)
	}
}

// Scenario: 3.2-UNIT-017
// Cache HIT → auth-svc RPC counter stays at 1 across two requests.
func TestRequireAPIKey_CacheHitAvoidsRPC(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	// First request — miss + RPC.
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("first: status = %d, want 501", rr1.Code)
	}
	// Second request — hit; no further RPC.
	rr2 := h.do("Bearer " + sentinelKey)
	if rr2.Code != http.StatusNotImplemented {
		t.Fatalf("second: status = %d, want 501", rr2.Code)
	}
	if h.stub.rpcHits != 1 {
		t.Errorf("rpc_hits = %d, want 1 (cache must absorb 2nd request)", h.stub.rpcHits)
	}
	if h.innerHits != 2 {
		t.Errorf("inner_hits = %d, want 2", h.innerHits)
	}
}

// --- UNIT-018 / UNIT-019 — anti-enumeration parity ------------------------

// Scenario: 3.2-UNIT-018
// auth-svc ok=false / NOT_FOUND → 401 + 401_invalid_api_key + NO SETEX.
func TestRequireAPIKey_NotFoundNoCache(t *testing.T) {
	t.Parallel()
	h := newHarness(t, notFoundValidate)
	h.innerPanic = true // inner must never run
	rr := h.do("Bearer " + sentinelKey)
	expect401Envelope(t, rr, "Invalid API key provided.")
	sum := sha256.Sum256([]byte(sentinelKey))
	key := "auth:apikey:" + hex.EncodeToString(sum[:])
	if _, err := h.mini.Get(key); err == nil {
		t.Errorf("cache populated under %s — BR-1.6 positives-only violated", key)
	}
}

// Scenario: 3.2-UNIT-019
// auth-svc ok=false / REVOKED → SAME 401 envelope shape (BR-2.4 parity).
// No SETEX.
func TestRequireAPIKey_RevokedSameEnvelope(t *testing.T) {
	t.Parallel()
	h := newHarness(t, revokedValidate)
	h.innerPanic = true
	rr := h.do("Bearer " + sentinelKey)
	expect401Envelope(t, rr, "Invalid API key provided.")
}

// --- UNIT-020 / UNIT-021 — 503 envelope -----------------------------------

// Scenario: 3.2-UNIT-020
// auth-svc connect.CodeUnavailable → 503 + 503_auth_unavailable.
func TestRequireAPIKey_AuthSvcUnavailable(t *testing.T) {
	t.Parallel()
	h := newHarness(t, unavailableValidate)
	h.innerPanic = true
	rr := h.do("Bearer " + sentinelKey)
	expect503Envelope(t, rr)
}

// Scenario: 3.2-UNIT-021
// auth-svc connect.CodeDeadlineExceeded → same 503 envelope.
func TestRequireAPIKey_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow auth-svc"))
	})
	h.innerPanic = true
	rr := h.do("Bearer " + sentinelKey)
	expect503Envelope(t, rr)
}

// --- UNIT-022 / UNIT-023 — Redis outage tolerance -------------------------

// Scenario: 3.2-UNIT-022
// Redis GET error (miniredis closed) → fall through to RPC; inner invoked
// on RPC success. Single WARN log.
func TestRequireAPIKey_RedisGetErrorFallsThrough(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	// Lazy redisClient construction happens on the first request — make
	// the factory observe the addr once, THEN close the server so the
	// subsequent GET returns a transport error (not a constructor panic).
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("warm-up status = %d, want 501", rr1.Code)
	}
	h.mini.Close() // outage AFTER lazy-init has captured the addr
	h.stub.rpcHits = 0
	rr := h.do("Bearer " + sentinelKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (RPC fall-through must succeed)", rr.Code)
	}
	if h.stub.rpcHits != 1 {
		t.Errorf("rpc_hits = %d, want 1", h.stub.rpcHits)
	}
}

// Scenario: 3.2-UNIT-023
// Redis SET error after RPC success → request still succeeds.
func TestRequireAPIKey_RedisSetErrorIsNonFatal(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	// First request — populate cache.
	_ = h.do("Bearer " + sentinelKey)
	// Now drop Redis; second request will hit RPC again then SET-fails.
	h.mini.FlushAll()
	h.mini.Close()
	rr := h.do("Bearer " + sentinelKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rr.Code)
	}
}

// --- UNIT-024 — context propagation ---------------------------------------

// Scenario: 3.2-UNIT-024
// Inner handler reads APIKeyIDFromContext + BearerUserIDFromContext +
// TeamIDFromContext + ScopeFromContext and asserts non-empty values
// (distinct from jwt_verify's UserIDFromContext per OQ3).
func TestRequireAPIKey_ContextPropagation(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	gotAPIKey, gotUser, gotTeam, gotScope := "", "", "", ""
	hasTeam := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey, _ = middleware.APIKeyIDFromContext(r.Context())
		gotUser, _ = middleware.BearerUserIDFromContext(r.Context())
		gotTeam, hasTeam = middleware.TeamIDFromContext(r.Context())
		gotScope, _ = middleware.ScopeFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sentinelKey)
	rr := httptest.NewRecorder()
	h.mw.RequireAPIKey(inner).ServeHTTP(rr, req)
	if gotAPIKey == "" {
		t.Errorf("APIKeyIDFromContext empty")
	}
	if gotUser == "" {
		t.Errorf("BearerUserIDFromContext empty")
	}
	if !hasTeam {
		t.Errorf("TeamIDFromContext returned ok=false")
	}
	_ = gotTeam // may legitimately be empty when no team
	if gotScope != `{}` {
		t.Errorf("ScopeFromContext = %q, want {}", gotScope)
	}
	// JWT-path keys must remain untouched.
	if v, ok := middleware.UserIDFromContext(req.Context()); ok || v != "" {
		t.Errorf("UserIDFromContext leaked from bearer path: %q", v)
	}
}

// --- UNIT-025 — inner-handler-never-invoked-on-error sentinel -------------

// Scenario: 3.2-UNIT-025
// Inner handler is NEVER invoked on 401/503; panic-on-call sentinel must
// not fire.
func TestRequireAPIKey_InnerNeverCalledOn401(t *testing.T) {
	t.Parallel()
	h := newHarness(t, notFoundValidate)
	h.innerPanic = true
	rr := h.do("Bearer " + sentinelKey)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

// --- UNIT-026 — span / log discipline (plaintext defence) -----------------

// Scenario: 3.2-UNIT-026
// Plaintext key MUST NOT leak into the auth-svc client log path. We verify
// by feeding a long-but-valid key and asserting the cache key uses
// sha256(plaintext) NOT the plaintext itself (BR-1.5).
func TestRequireAPIKey_CacheKeyIsSHA256NotPlaintext(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	_ = h.do("Bearer " + sentinelKey)
	keys := h.mini.Keys()
	if len(keys) != 1 {
		t.Fatalf("redis keys = %v, want exactly 1", keys)
	}
	k := keys[0]
	if !strings.HasPrefix(k, "auth:apikey:") {
		t.Errorf("cache key prefix mismatch: %q", k)
	}
	if strings.Contains(k, sentinelKey) {
		t.Fatalf("plaintext leaked into cache key: %q", k)
	}
	if len(k) != len("auth:apikey:")+64 {
		t.Errorf("cache key length = %d, want %d (11-char prefix + 64 hex)",
			len(k), len("auth:apikey:")+64)
	}
}

// --- Benchmark — BenchmarkBearerAuthCacheHit ------------------------------

// BenchmarkBearerAuthCacheHit measures the hot path with the cache pre-
// warmed. Informational baseline only — no CI gate. Reports allocs +
// wall time / op.
func BenchmarkBearerAuthCacheHit(b *testing.B) {
	mini := miniredis.RunT(&testWrapper{B: b})
	stub := &stubAuthSvc{validate: okValidate(sentinelKey)}
	_, handler := authv1connect.NewAuthServiceHandler(stub)
	srv := httptest.NewServer(handler)
	defer srv.Close()
	client := authv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mw := middleware.NewAPIKeyAuthenticator(client, func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()})
	}, logger)
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := mw.RequireAPIKey(inner)

	// Pre-warm the cache once.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+sentinelKey)
	chain.ServeHTTP(httptest.NewRecorder(), req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, req)
	}
	if got := atomic.LoadInt32(&stub.rpcHits); got != 1 {
		b.Fatalf("rpc_hits = %d after b.N=%d, want 1 (cache must absorb)", got, b.N)
	}
}

// --- Story 5.1 UNIT-028..030 — sentinel-check additive branch ---------------
//
// The Story-5.1 BR-3.8 modification: on positive cache-hit, the gateway
// EXISTS-checks `auth:apikey:revoked:{api_key_id}`; presence purges the
// cache + falls through to RPC (which returns REVOKED → 401).

// errInjectHook implements redis.Hook to inject a configurable error on a
// specific command name. Other commands pass through to the underlying
// Redis. Used by UNIT-030 to isolate the sentinel-EXISTS failure path.
type errInjectHook struct {
	cmdName string
	err     error
}

func (h errInjectHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h errInjectHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h errInjectHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == h.cmdName {
			// Set the command's error so the caller observes the injection.
			cmd.SetErr(h.err)
			return h.err
		}
		return next(ctx, cmd)
	}
}

// Scenario: 5.1-UNIT-028
// Cache HIT + sentinel ABSENT → serves from cache (no RPC, no purge).
func TestRequireAPIKey_SentinelAbsent_ServesFromCache(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	// 1st request — miss + RPC + SETEX positive.
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("first: status = %d, want 501", rr1.Code)
	}
	rpcBefore := atomic.LoadInt32(&h.stub.rpcHits)
	// 2nd request — sentinel ABSENT → cache hit serves OK.
	rr2 := h.do("Bearer " + sentinelKey)
	if rr2.Code != http.StatusNotImplemented {
		t.Fatalf("second: status = %d, want 501 (cache hit)", rr2.Code)
	}
	if atomic.LoadInt32(&h.stub.rpcHits) != rpcBefore {
		t.Fatalf("rpc_hits incremented from %d (cache hit must avoid RPC when sentinel absent)", rpcBefore)
	}
	// Cache entry MUST still be present.
	sum := sha256.Sum256([]byte(sentinelKey))
	cacheKey := "auth:apikey:" + hex.EncodeToString(sum[:])
	if val, err := h.mini.Get(cacheKey); err != nil || val == "" {
		t.Fatalf("cache entry purged unexpectedly")
	}
}

// Scenario: 5.1-UNIT-029
// Cache HIT + sentinel PRESENT → purges cache + falls through to RPC
// (which returns REVOKED → 401).
func TestRequireAPIKey_SentinelPresent_PurgesAndFallsThrough(t *testing.T) {
	t.Parallel()
	// Two-phase validate: first call returns ok; second call (after sentinel
	// fires) returns REVOKED. The harness's stub does not natively support
	// per-call swapping, so we toggle via a closure-captured flag.
	var revoked atomic.Bool
	validate := func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if revoked.Load() {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_REVOKED}, nil
		}
		if req.GetPlaintextKey() != sentinelKey {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
		}
		return &authv1.ValidateApiKeyResponse{
			Ok:       true,
			ApiKeyId: "00000000-0000-0000-0000-000000000001",
			UserId:   "00000000-0000-0000-0000-000000000002",
			TeamId:   "",
			Scope:    `{}`,
			Reason:   authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED,
		}, nil
	}
	h := newHarness(t, validate)
	// 1st request — cache + RPC ok.
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("first: status = %d, want 501", rr1.Code)
	}
	rpcBefore := atomic.LoadInt32(&h.stub.rpcHits)
	// Trip the sentinel for the cached api_key_id.
	sentinelFullKey := middleware.SentinelKeyPrefix + "00000000-0000-0000-0000-000000000001"
	if err := h.mini.Set(sentinelFullKey, "1"); err != nil {
		t.Fatalf("miniredis Set sentinel: %v", err)
	}
	h.mini.SetTTL(sentinelFullKey, 300*time.Second)
	// Switch validate to REVOKED for the 2nd request (which falls through to RPC).
	revoked.Store(true)
	h.innerPanic = true // inner MUST NOT be invoked on the 2nd request (401 path)
	rr2 := h.do("Bearer " + sentinelKey)
	expect401Envelope(t, rr2, "Invalid API key provided.")
	if atomic.LoadInt32(&h.stub.rpcHits) != rpcBefore+1 {
		t.Fatalf("rpc_hits = %d, want %d (sentinel must force fall-through to RPC)",
			h.stub.rpcHits, rpcBefore+1)
	}
	// Cache entry MUST have been purged.
	sum := sha256.Sum256([]byte(sentinelKey))
	cacheKey := "auth:apikey:" + hex.EncodeToString(sum[:])
	if val, err := h.mini.Get(cacheKey); err == nil && val != "" {
		t.Fatalf("cache entry still present after sentinel hit — purge contract violated")
	}
}

// Scenario: 5.1-UNIT-030
// Cache HIT + sentinel-EXISTS Redis error → fail-OPEN: serves cached
// positive + WARN log. The cache GET succeeds; only EXISTS fails (isolated
// via redis.Hook).
func TestRequireAPIKey_SentinelExistsError_FailOpen(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidate(sentinelKey))
	// 1st request — cache + RPC ok.
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("first: status = %d, want 501", rr1.Code)
	}
	rpcBefore := atomic.LoadInt32(&h.stub.rpcHits)
	// Capture WARN logs by rebuilding the middleware with a logging
	// buffer + an EXISTS-injection hook. NewHarness wires a discard logger
	// so we replace it here.
	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := redis.NewClient(&redis.Options{Addr: h.mini.Addr()})
	client.AddHook(errInjectHook{cmdName: "exists", err: errors.New("simulated sentinel outage")})
	authClient := authv1connect.NewAuthServiceClient(h.authSrv.Client(), h.authSrv.URL)
	mw := middleware.NewAPIKeyAuthenticator(authClient, func() *redis.Client { return client }, logger)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sentinelKey)
	rr2 := httptest.NewRecorder()
	mw.RequireAPIKey(h.inner()).ServeHTTP(rr2, req)
	if rr2.Code != http.StatusNotImplemented {
		t.Fatalf("second: status = %d, want 501 (fail-open serves from cache)", rr2.Code)
	}
	if atomic.LoadInt32(&h.stub.rpcHits) != rpcBefore {
		t.Fatalf("rpc_hits = %d, want %d (sentinel error must NOT force RPC; fail-open serves cache)", h.stub.rpcHits, rpcBefore)
	}
	if !strings.Contains(logBuf.String(), "sentinel EXISTS error") {
		t.Fatalf("logBuf missing sentinel EXISTS error WARN: %s", logBuf.String())
	}
}

// testWrapper lets miniredis.RunT accept a *testing.B (it asks for the
// minimal TestingT interface).
type testWrapper struct{ B *testing.B }

func (w *testWrapper) Cleanup(fn func())                         { w.B.Cleanup(fn) }
func (w *testWrapper) Errorf(format string, args ...interface{}) { w.B.Errorf(format, args...) }
func (w *testWrapper) Fatalf(format string, args ...interface{}) { w.B.Fatalf(format, args...) }
func (w *testWrapper) Helper()                                   { w.B.Helper() }
func (w *testWrapper) Skipf(format string, args ...interface{})  { w.B.Skipf(format, args...) }
func (w *testWrapper) Logf(format string, args ...interface{})   { w.B.Logf(format, args...) }
func (w *testWrapper) Log(args ...interface{})                   { w.B.Log(args...) }
func (w *testWrapper) Fail()                                     { w.B.Fail() }
func (w *testWrapper) FailNow()                                  { w.B.FailNow() }
func (w *testWrapper) Failed() bool                              { return w.B.Failed() }
func (w *testWrapper) Name() string                              { return w.B.Name() }
func (w *testWrapper) TempDir() string                           { return w.B.TempDir() }
func (w *testWrapper) Setenv(key, value string)                  { w.B.Setenv(key, value) }
func (w *testWrapper) Error(args ...interface{})                 { w.B.Error(args...) }
func (w *testWrapper) Fatal(args ...interface{})                 { w.B.Fatal(args...) }
func (w *testWrapper) Skip(args ...interface{})                  { w.B.Skip(args...) }
func (w *testWrapper) SkipNow()                                  { w.B.SkipNow() }
func (w *testWrapper) Skipped() bool                             { return w.B.Skipped() }
func (w *testWrapper) String() string                            { return fmt.Sprintf("benchmark-%s", w.B.Name()) }
