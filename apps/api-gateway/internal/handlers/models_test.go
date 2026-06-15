// Story 3.5 — Tests for /v1/models handler.
//
// Source: docs/qa/assessments/3.5-test-design-20260519.md
//
// Architect Round 1 OQ1 ruling: 11-entry canonical catalogue (NOT 9).
// Architect Round 1 OQ2 ruling: sibling constant MaxEmbeddingBodyBytes
// (see embeddings_test.go).
//
// Package = handlers (in-package) — required for direct access to
// unexported items (modelsCatalogue, NewModelsHandler internals tested via
// WithModelsNow option, etc.).
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// ----- Shared test helpers ----------------------------------------------

const (
	modelsTestAPIKeyID = "11111111-1111-1111-1111-111111111111"
)

// modelsAuthedCtx attaches the bearer-auth context keys the Story-3.2
// middleware would inject. Tests focused on the handler internals use this
// shortcut; full chain coverage lives in INT-002 / INT-003.
func modelsAuthedCtx(ctx context.Context) context.Context {
	ctx = middleware.WithAPIKeyID(ctx, modelsTestAPIKeyID)
	ctx = middleware.BearerWithUserID(ctx, "22222222-2222-2222-2222-222222222222")
	ctx = middleware.WithTeamID(ctx, "")
	ctx = middleware.WithScope(ctx, `{}`)
	return ctx
}

// fixedClock returns a func returning a constant t (test-only).
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// silentLogger returns a slog.Logger that discards output (used when the
// log contents are not the test's load-bearing assertion).
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// doModelsGet invokes h.ServeHTTP with the supplied context wrapping a
// bare GET /v1/models request and returns the recorder.
func doModelsGet(h *ModelsHandler, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// ============================================================
// AC1.A — Static Catalogue Invariants (Unit)
// ============================================================

// Scenario: 3.5-UNIT-001
// Priority: P0  ·  Level: unit
func Test_ModelsCatalogue_BR_1_4_invariants(t *testing.T) {
	t.Parallel()

	if got := len(modelsCatalogue); got != 13 {
		t.Fatalf("len(modelsCatalogue) = %d, want 13 (Architect OQ1 + 2 Vision models, Story 9.5)", got)
	}

	idRe := regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*$`)
	allowedOwners := map[string]struct{}{
		"alibaba": {}, "deepseek": {}, "moonshot": {}, "zhipu": {},
		"bytedance": {}, "baidu": {}, "he-api": {},
	}

	wantIDs := []string{
		"qwen-max", "qwen-plus", "deepseek-v3", "moonshot-v1-128k",
		"glm-4", "doubao-pro", "doubao-lite", "ernie-4.0",
		"he-router-cost", "he-router-quality", "he-router-latency",
		"qwen-vl-max", "glm-4v", // Story 9.5 — appended (preserves indices 0-10)
	}
	for i, want := range wantIDs {
		got := modelsCatalogue[i]
		if got.ID != want {
			t.Errorf("modelsCatalogue[%d].ID = %q, want %q", i, got.ID, want)
		}
		if got.Object != "model" {
			t.Errorf("modelsCatalogue[%d].Object = %q, want \"model\"", i, got.Object)
		}
		if !idRe.MatchString(got.ID) {
			t.Errorf("modelsCatalogue[%d].ID %q does not match %s", i, got.ID, idRe)
		}
		if _, ok := allowedOwners[got.OwnedBy]; !ok {
			t.Errorf("modelsCatalogue[%d].OwnedBy = %q, not in BR-1.5 closed-set", i, got.OwnedBy)
		}
	}
}

// ============================================================
// AC1.B — Handler Construction (Unit)
// ============================================================

// Scenario: 3.5-UNIT-002
// Priority: P0  ·  Level: unit
func Test_NewModelsHandler_WithNow_overrides_clock(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, time.May, 19, 12, 0, 0, 0, time.UTC)
	h := NewModelsHandler(silentLogger(), WithModelsNow(fixedClock(fixed)))
	if got := h.now(); !got.Equal(fixed) {
		t.Errorf("h.now() = %v, want %v", got, fixed)
	}
}

// Scenario: 3.5-UNIT-003
// Priority: P0  ·  Level: unit
func Test_NewModelsHandler_captures_startedAt_once(t *testing.T) {
	t.Parallel()
	var calls int32
	base := time.Date(2026, time.May, 19, 12, 0, 0, 0, time.UTC)
	stub := func() time.Time {
		n := atomic.AddInt32(&calls, 1)
		// distinct timestamp every call so a regression that re-reads
		// the clock per request would surface as a different `created`.
		return base.Add(time.Duration(n) * time.Second)
	}
	h := NewModelsHandler(silentLogger(), WithModelsNow(stub))

	// Constructor MUST have read the clock exactly once.
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("constructor invoked now() %d times, want 1", got)
	}
	wantStarted := base.Add(1 * time.Second).Unix()
	if h.startedAt != wantStarted {
		t.Errorf("h.startedAt = %d, want %d (first call)", h.startedAt, wantStarted)
	}

	// Issue two requests; the responses must use h.startedAt — NOT subsequent
	// now() values — proving ServeHTTP does NOT call h.now().
	r1 := doModelsGet(h, modelsAuthedCtx(context.Background()))
	r2 := doModelsGet(h, modelsAuthedCtx(context.Background()))
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("ServeHTTP invoked now() (total calls = %d); want still 1", got)
	}
	for i, rr := range []*httptest.ResponseRecorder{r1, r2} {
		var resp ModelsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response %d unmarshal: %v", i, err)
		}
		for j, e := range resp.Data {
			if e.Created != wantStarted {
				t.Errorf("response %d entry %d created = %d, want %d", i, j, e.Created, wantStarted)
			}
		}
	}
}

// ============================================================
// AC1.C — Defence-in-Depth & Logging (Unit)
// ============================================================

// Scenario: 3.5-UNIT-004
// Priority: P0  ·  Level: unit
func Test_ModelsHandler_unwired_middleware_returns_500_gateway_misconfigured(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil) // bare ctx, no APIKeyID
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body unmarshal: %v\nbody=%s", err, rr.Body.String())
	}
	errMap, _ := env["error"].(map[string]any)
	if errMap == nil {
		t.Fatalf("body missing error envelope: %s", rr.Body.String())
	}
	if got, _ := errMap["code"].(string); got != "500_gateway_misconfigured" {
		t.Errorf("error.code = %q, want %q", got, "500_gateway_misconfigured")
	}
	if got, _ := errMap["message"].(string); got != "Bearer-auth middleware not wired" {
		t.Errorf("error.message = %q", got)
	}
}

// recordingHandler captures every slog.Record (and its attributes) for
// assertion. Used by UNIT-005 + UNIT-023 (PII discipline).
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr // attrs from WithAttrs chain
}

func (r *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Materialize the WithAttrs chain into the record so callers can scan
	// every attribute without walking the parent chain.
	for _, a := range r.attrs {
		rec.AddAttrs(a)
	}
	r.records = append(r.records, rec)
	return nil
}

func (r *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{records: nil, attrs: append(append([]slog.Attr{}, r.attrs...), attrs...)}
}
func (r *recordingHandler) WithGroup(_ string) slog.Handler { return r }

// flatAttrs walks a slog.Record's attributes (including grouped) and
// returns the flat list. Used by PII tests to scan every attribute Value.
func flatAttrs(rec slog.Record) []slog.Attr {
	var out []slog.Attr
	rec.Attrs(func(a slog.Attr) bool {
		out = append(out, a)
		return true
	})
	return out
}

// Scenario: 3.5-UNIT-005
// Priority: P0  ·  Level: unit
func Test_ModelsHandler_emits_exactly_one_log_line_per_request(t *testing.T) {
	t.Parallel()
	rh := &recordingHandler{}
	h := NewModelsHandler(slog.New(rh))
	rr := doModelsGet(h, modelsAuthedCtx(context.Background()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := len(rh.records); got != 1 {
		t.Fatalf("len(records) = %d, want 1", got)
	}
	rec := rh.records[0]
	// Story-4.7 BR-1.9 — event renamed `models_list` → `models_list_v1`.
	if rec.Message != "models_list_v1" {
		t.Errorf("record.Message = %q, want \"models_list_v1\"", rec.Message)
	}
	gotAttrs := map[string]any{}
	for _, a := range flatAttrs(rec) {
		gotAttrs[a.Key] = a.Value.Any()
	}
	if v, _ := gotAttrs["event"].(string); v != "models_list_v1" {
		t.Errorf("attr event = %v, want \"models_list_v1\"", gotAttrs["event"])
	}
	if v, _ := gotAttrs["api_key_id"].(string); v != modelsTestAPIKeyID {
		t.Errorf("attr api_key_id = %v, want %q", gotAttrs["api_key_id"], modelsTestAPIKeyID)
	}
	if v, _ := gotAttrs["catalogue_size"].(int64); v != 13 {
		// slog.Int stores as int64
		t.Errorf("attr catalogue_size = %v, want 13", gotAttrs["catalogue_size"])
	}
}

// ============================================================
// AC1.D — Handler Integration via httptest (Integration)
// ============================================================

// Scenario: 3.5-INT-001
// Priority: P0  ·  Level: integration
func Test_ModelsHandler_returns_OpenAI_ModelList_shape_ordering(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())
	rr := doModelsGet(h, modelsAuthedCtx(context.Background()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var resp ModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, rr.Body.String())
	}
	if resp.Object != "list" {
		t.Errorf("response.object = %q, want \"list\"", resp.Object)
	}
	if len(resp.Data) != len(modelsCatalogue) {
		t.Fatalf("len(data) = %d, want %d", len(resp.Data), len(modelsCatalogue))
	}
	for i := range modelsCatalogue {
		if resp.Data[i].ID != modelsCatalogue[i].ID {
			t.Errorf("data[%d].id = %q, want %q (BR-1.10 ordering)",
				i, resp.Data[i].ID, modelsCatalogue[i].ID)
		}
		if resp.Data[i].OwnedBy != modelsCatalogue[i].OwnedBy {
			t.Errorf("data[%d].owned_by = %q, want %q",
				i, resp.Data[i].OwnedBy, modelsCatalogue[i].OwnedBy)
		}
	}
}

// ----- Wire-level bearer-auth chain helpers (in-package) ----------------

// stubAuthSvc is a connect-go AuthService stub whose ValidateApiKey
// delegates to the test-supplied func. Mirrors the chat_completions_test
// chatStubAuthSvc inline so the in-package file remains self-contained.
type stubAuthSvc struct {
	authv1connect.UnimplementedAuthServiceHandler
	validateFn func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
}

func (s *stubAuthSvc) ValidateApiKey(
	_ context.Context,
	req *connect.Request[authv1.ValidateApiKeyRequest],
) (*connect.Response[authv1.ValidateApiKeyResponse], error) {
	resp, err := s.validateFn(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// newModelsBearerHarness spins up an in-test miniredis + a stubbed auth-svc
// and returns a configured APIKeyAuthenticator. Mirrors
// chat_completions_test.newBearerHarness but lives here so the in-package
// suite stays self-contained.
func newModelsBearerHarness(
	t *testing.T,
	validate func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error),
) *middleware.APIKeyAuthenticator {
	t.Helper()
	mini := miniredis.RunT(t)
	stub := &stubAuthSvc{validateFn: validate}
	_, h := authv1connect.NewAuthServiceHandler(stub)
	authSrv := httptest.NewServer(h)
	t.Cleanup(authSrv.Close)
	client := authv1connect.NewAuthServiceClient(authSrv.Client(), authSrv.URL)
	return middleware.NewAPIKeyAuthenticator(client, func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()})
	}, silentLogger())
}

// Scenario: 3.5-INT-002
// Priority: P0  ·  Level: integration
// Full bearer-auth chain via miniredis + connect-go stub. Asserts the
// wrapped handler emits 200 + Content-Type, and the upstream-injected
// api_key_id flows through to the handler's log line.
func Test_ModelsHandler_with_valid_bearer_returns_200(t *testing.T) {
	t.Parallel()
	const plaintext = "he-INT002MODELSXYZ12345"
	const apiKeyID = "33333333-3333-3333-3333-333333333333"

	mw := newModelsBearerHarness(t, func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if req.GetPlaintextKey() != plaintext {
			return &authv1.ValidateApiKeyResponse{
				Ok:     false,
				Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND,
			}, nil
		}
		return &authv1.ValidateApiKeyResponse{
			Ok: true, ApiKeyId: apiKeyID, UserId: "44444444-4444-4444-4444-444444444444", Scope: `{}`,
		}, nil
	})

	buf := &bytes.Buffer{}
	h := NewModelsHandler(slog.New(slog.NewJSONHandler(buf, nil)))
	wrapped := mw.RequireAPIKey(h)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(buf.String(), `"api_key_id":"`+apiKeyID+`"`) {
		t.Errorf("log missing api_key_id=%s: %s", apiKeyID, buf.String())
	}
}

// Scenario: 3.5-INT-003
// Priority: P0  ·  Level: integration
// No Authorization header → 401 + Story-3.2 envelope. The inner handler
// MUST NOT run — verified by wiring a sentinel that panics on invocation.
func Test_ModelsHandler_missing_bearer_returns_401(t *testing.T) {
	t.Parallel()
	mw := newModelsBearerHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return &authv1.ValidateApiKeyResponse{Ok: false}, nil
	})

	var innerCalls atomic.Int32
	sentinel := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalls.Add(1)
		panic("models handler MUST NOT be invoked when auth fails — wiring regression")
	})
	wrapped := mw.RequireAPIKey(sentinel)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil) // no Authorization
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("envelope unmarshal: %v\nbody=%s", err, body)
	}
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "401_invalid_api_key" {
		t.Errorf("error.code = %q, want \"401_invalid_api_key\"", got)
	}
	if innerCalls.Load() != 0 {
		t.Errorf("inner handler was invoked despite auth failure (calls=%d)", innerCalls.Load())
	}
}

// Scenario: 3.5-INT-004
// Priority: P0  ·  Level: integration
func Test_ModelsHandler_POST_returns_405_with_Allow_GET(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())
	mux := http.NewServeMux()
	mux.Handle("GET /v1/models", h)

	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	// Go 1.22 stdlib auto-adds HEAD to GET-registered routes, so Allow
	// contains "GET, HEAD". QA spec INT-004 said "Allow: GET" — the
	// "(Go 1.22 stdlib behaviour)" qualifier reveals the intent: verify
	// the 405 is stdlib-emitted (not a custom envelope) AND signals GET.
	allow := rr.Header().Get("Allow")
	if !strings.Contains(allow, "GET") {
		t.Errorf("Allow header = %q, want to contain \"GET\"", allow)
	}
}

// Scenario: 3.5-INT-005
// Priority: P0  ·  Level: integration
func Test_ModelsHandler_stable_created_across_requests(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, time.May, 19, 12, 0, 0, 0, time.UTC)
	h := NewModelsHandler(silentLogger(), WithModelsNow(fixedClock(fixed)))

	r1 := doModelsGet(h, modelsAuthedCtx(context.Background()))
	r2 := doModelsGet(h, modelsAuthedCtx(context.Background()))

	var resp1, resp2 ModelsResponse
	if err := json.Unmarshal(r1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("resp1: %v", err)
	}
	if err := json.Unmarshal(r2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("resp2: %v", err)
	}
	if len(resp1.Data) != len(resp2.Data) {
		t.Fatalf("len mismatch: %d vs %d", len(resp1.Data), len(resp2.Data))
	}
	for i := range resp1.Data {
		if resp1.Data[i].Created != resp2.Data[i].Created {
			t.Errorf("entry %d created: r1=%d, r2=%d (BR-1.6 stable invariant)",
				i, resp1.Data[i].Created, resp2.Data[i].Created)
		}
		if resp1.Data[i].Created != fixed.Unix() {
			t.Errorf("entry %d created = %d, want %d", i, resp1.Data[i].Created, fixed.Unix())
		}
	}
}

// ============================================================
// Blind-Spot Scenarios for /v1/models
// ============================================================

// Scenario: 3.5-BLIND-CONCURRENCY-001  [BLIND-SPOT CONCURRENCY-002]
// Priority: P1  ·  Level: integration
func Test_ModelsHandler_concurrent_100_requests_stable_created(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, time.May, 19, 12, 0, 0, 0, time.UTC)
	h := NewModelsHandler(silentLogger(), WithModelsNow(fixedClock(fixed)))

	const N = 100
	results := make([][]ModelEntry, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			rr := doModelsGet(h, modelsAuthedCtx(context.Background()))
			var resp ModelsResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Errorf("g%d unmarshal: %v", idx, err)
				return
			}
			results[idx] = resp.Data
		}(i)
	}
	wg.Wait()

	want := fixed.Unix()
	for i := 0; i < N; i++ {
		if len(results[i]) != len(modelsCatalogue) {
			t.Errorf("g%d data len = %d", i, len(results[i]))
			continue
		}
		for j, e := range results[i] {
			if e.Created != want {
				t.Errorf("g%d entry %d created = %d, want %d", i, j, e.Created, want)
			}
		}
	}
}

// Scenario: 3.5-BLIND-DATA-001  [BLIND-SPOT DATA-003]
// Priority: P1  ·  Level: unit
func Test_ModelsHandler_response_mutation_does_not_affect_package_slice(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, time.May, 19, 12, 0, 0, 0, time.UTC)
	h := NewModelsHandler(silentLogger(), WithModelsNow(fixedClock(fixed)))
	originalCreated := modelsCatalogue[0].Created // expect 0 (zero value)

	// Issue first request, parse, then mutate the parsed slice. This
	// simulates a future bug where a downstream consumer mutates the
	// response — the package slice MUST be untouched.
	r1 := doModelsGet(h, modelsAuthedCtx(context.Background()))
	var resp1 ModelsResponse
	if err := json.Unmarshal(r1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("resp1: %v", err)
	}
	resp1.Data[0].Created = 999
	// Also directly mutate the handler-returned slice (no JSON round-trip)
	// by issuing another call into a shim that exposes the raw slice.
	// Since the response writer copies via JSON, the more meaningful guard
	// is that modelsCatalogue itself is unchanged.

	if modelsCatalogue[0].Created != originalCreated {
		t.Errorf("modelsCatalogue[0].Created mutated to %d (was %d)",
			modelsCatalogue[0].Created, originalCreated)
	}

	// Second request must still return h.startedAt — not 999.
	r2 := doModelsGet(h, modelsAuthedCtx(context.Background()))
	var resp2 ModelsResponse
	if err := json.Unmarshal(r2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("resp2: %v", err)
	}
	if resp2.Data[0].Created != fixed.Unix() {
		t.Errorf("resp2.Data[0].Created = %d, want %d (mutation leaked into package slice)",
			resp2.Data[0].Created, fixed.Unix())
	}
}

// ============================================================
// Story 4.7 — Capability extension on /v1/models
// ============================================================

// Scenario: 4.7-UNIT-001
// Priority: P0  ·  Level: unit
//
// Bearer-authed GET /v1/models returns a `capabilities` object on every
// entry (non-null, present on each of the 11 rows). BR-1.1 load-bearing.
func Test_ModelsHandler_response_carries_capabilities_field_on_every_entry(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())
	rr := doModelsGet(h, modelsAuthedCtx(context.Background()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Parse as raw JSON so a stray `null` or omission can be detected
	// without the Go-side struct silently defaulting it to a zero-value
	// ModelCapabilities{}.
	var envelope struct {
		Object string                   `json:"object"`
		Data   []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, rr.Body.String())
	}
	if got, want := len(envelope.Data), 13; got != want {
		t.Fatalf("len(data) = %d, want %d", got, want)
	}
	for i, e := range envelope.Data {
		v, present := e["capabilities"]
		if !present {
			t.Errorf("data[%d] (id=%v) missing `capabilities` field", i, e["id"])
			continue
		}
		obj, ok := v.(map[string]interface{})
		if !ok || obj == nil {
			t.Errorf("data[%d] (id=%v) capabilities = %v (%T), want non-null JSON object",
				i, e["id"], v, v)
		}
	}
}

// Scenario: 4.7-UNIT-002
// Priority: P0  ·  Level: unit
//
// Table-driven across the 11 catalogue IDs: each entry's capabilities
// deep-equals the BR-1.3 golden struct verbatim. The 11-row table is the
// load-bearing precedent Story 4.8 contract tests will cascade.
func Test_ModelsHandler_capabilities_match_BR_1_3_verbatim(t *testing.T) {
	t.Parallel()
	golden := map[string]ModelCapabilities{
		"qwen-max":          {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"qwen-plus":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"deepseek-v3":       {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 65536, MaxOutputTokens: 8192},
		"moonshot-v1-128k":  {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"glm-4":             {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"doubao-pro":        {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"doubao-lite":       {Chat: true, Streaming: true, FunctionCalling: false, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 4096},
		"ernie-4.0":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 8192, MaxOutputTokens: 2048},
		"he-router-cost":    {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"he-router-quality": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"he-router-latency": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		// Story 9.5 — the 2 Vision models (Vision:true). Values mirror
		// packages/models-catalogue/registry.go verbatim.
		"qwen-vl-max": {Chat: true, Streaming: true, FunctionCalling: false, Vision: true, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"glm-4v":      {Chat: true, Streaming: true, FunctionCalling: false, Vision: true, JSONMode: false, ContextWindowTokens: 8192, MaxOutputTokens: 4096},
	}

	h := NewModelsHandler(silentLogger())
	rr := doModelsGet(h, modelsAuthedCtx(context.Background()))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp ModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != len(golden) {
		t.Fatalf("len(data) = %d, want %d", len(resp.Data), len(golden))
	}
	for _, entry := range resp.Data {
		want, ok := golden[entry.ID]
		if !ok {
			t.Errorf("unexpected model id %q in response", entry.ID)
			continue
		}
		if entry.Capabilities != want {
			t.Errorf("id=%s capabilities = %#v, want %#v",
				entry.ID, entry.Capabilities, want)
		}
	}
}

// Scenario: 4.7-UNIT-003
// Priority: P0  ·  Level: unit
//
// Field-order regression guard. encoding/json marshals struct fields in
// declaration order; a future contributor alphabetising or reordering
// fields would silently break SDK consumers using strict-order parsers.
func Test_ModelEntry_JSON_field_order_is_canonical_BR_1_4(t *testing.T) {
	t.Parallel()
	entry := ModelEntry{
		ID:      "qwen-max",
		Object:  "model",
		Created: 1700000000,
		OwnedBy: "alibaba",
		Capabilities: ModelCapabilities{
			Chat: true, Streaming: true, FunctionCalling: true,
			Vision: false, JSONMode: true,
			ContextWindowTokens: 32768, MaxOutputTokens: 8192,
		},
	}
	buf, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// id → object → created → owned_by → capabilities (LAST). The capabilities
	// object's own internal order is also asserted: BR-1.2.
	re := regexp.MustCompile(`^\{"id":"qwen-max","object":"model","created":1700000000,"owned_by":"alibaba","capabilities":\{"chat":true,"streaming":true,"function_calling":true,"vision":false,"json_mode":true,"context_window_tokens":32768,"max_output_tokens":8192\}\}$`)
	if !re.Match(buf) {
		t.Errorf("marshalled ModelEntry violates BR-1.4 field order\n  got: %s", buf)
	}
}

// Scenario: 4.7-UNIT-004
// Priority: P0  ·  Level: unit
//
// Story-3.5 BR-1.7 defence-in-depth regression check after the Capabilities
// extension. The unwired-middleware 500 envelope path MUST still fire
// unchanged.
func Test_ModelsHandler_unwired_middleware_returns_500_after_capabilities_extension(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil) // bare ctx, no APIKeyID
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body unmarshal: %v\nbody=%s", err, rr.Body.String())
	}
	errMap, _ := env["error"].(map[string]any)
	if errMap == nil {
		t.Fatalf("body missing error envelope: %s", rr.Body.String())
	}
	if got, _ := errMap["code"].(string); got != "500_gateway_misconfigured" {
		t.Errorf("error.code = %q, want %q", got, "500_gateway_misconfigured")
	}
}

// Scenario: 4.7-UNIT-005
// Priority: P0  ·  Level: unit
//
// BR-1.9 — slog test double asserts the renamed event and required attrs.
func Test_ModelsHandler_emits_models_list_v1_event(t *testing.T) {
	t.Parallel()
	rh := &recordingHandler{}
	h := NewModelsHandler(slog.New(rh))
	rr := doModelsGet(h, modelsAuthedCtx(context.Background()))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := len(rh.records); got != 1 {
		t.Fatalf("len(records) = %d, want 1", got)
	}
	rec := rh.records[0]
	if rec.Message != "models_list_v1" {
		t.Errorf("record.Message = %q, want \"models_list_v1\"", rec.Message)
	}
	gotAttrs := map[string]any{}
	for _, a := range flatAttrs(rec) {
		gotAttrs[a.Key] = a.Value.Any()
	}
	if v, _ := gotAttrs["event"].(string); v != "models_list_v1" {
		t.Errorf("attr event = %v, want \"models_list_v1\"", gotAttrs["event"])
	}
	if v, _ := gotAttrs["api_key_id"].(string); v != modelsTestAPIKeyID {
		t.Errorf("attr api_key_id = %v, want %q", gotAttrs["api_key_id"], modelsTestAPIKeyID)
	}
	if v, _ := gotAttrs["catalogue_size"].(int64); v != 13 {
		t.Errorf("attr catalogue_size = %v, want 13", gotAttrs["catalogue_size"])
	}
}

// Scenario: 4.7-UNIT-010
// Priority: P0  ·  Level: unit
//
// 1:1 invariant test (mirrors the init() panic) — if T0.4 init() is ever
// removed (e.g., during refactor) this test surfaces the same drift.
func Test_capabilitiesByModelID_1to1_with_modelsCatalogue(t *testing.T) {
	t.Parallel()
	if got, want := len(capabilitiesByModelID), len(modelsCatalogue); got != want {
		t.Errorf("len(capabilitiesByModelID) = %d, want %d (BR-1.3 1:1 invariant)",
			got, want)
	}
	for _, m := range modelsCatalogue {
		caps, ok := capabilitiesByModelID[m.ID]
		if !ok {
			t.Errorf("capabilitiesByModelID missing row for catalogue id %q", m.ID)
			continue
		}
		// Reject the zero-value ModelCapabilities{} sentinel — every catalogue
		// id has at least Chat=true per BR-1.3.
		zero := ModelCapabilities{}
		if caps == zero {
			t.Errorf("capabilitiesByModelID[%q] = zero value — likely uninitialised", m.ID)
		}
	}
}

// Scenario: 3.5-BLIND-FLOW-001  [BLIND-SPOT FLOW-005]
// Priority: P2  ·  Level: integration
func Test_ModelsHandler_ignores_query_string(t *testing.T) {
	t.Parallel()
	h := NewModelsHandler(silentLogger())

	bareReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	bareReq = bareReq.WithContext(modelsAuthedCtx(bareReq.Context()))
	bareRR := httptest.NewRecorder()
	h.ServeHTTP(bareRR, bareReq)

	qReq := httptest.NewRequest(http.MethodGet, "/v1/models?foo=bar&baz=qux", nil)
	qReq = qReq.WithContext(modelsAuthedCtx(qReq.Context()))
	qRR := httptest.NewRecorder()
	h.ServeHTTP(qRR, qReq)

	if bareRR.Code != http.StatusOK || qRR.Code != http.StatusOK {
		t.Fatalf("bare=%d query=%d, both want 200", bareRR.Code, qRR.Code)
	}
	if !bytes.Equal(bareRR.Body.Bytes(), qRR.Body.Bytes()) {
		t.Errorf("body differs with query string\nbare=%s\nwithq=%s",
			bareRR.Body.String(), qRR.Body.String())
	}
}
