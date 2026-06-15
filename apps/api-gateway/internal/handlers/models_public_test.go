// Story 4.7 — PublicModelsHandler unit tests.
//
// Covers:
//
//   4.7-UNIT-006 — method/header permutations (BR-1.7 + OQ-4.7-6 405 envelope)
//   4.7-UNIT-007 — slog event=models_list_public; NO api_key_id
//   4.7-BLIND-BOUNDARY-001 — HEAD method
//   4.7-BLIND-CONCURRENCY-001 — 100 concurrent GETs byte-identical
//   4.7-BLIND-CONCURRENCY-002 — concurrent /v1 + /public byte-identical
//   4.7-BLIND-DATA-001 — response mutation does not affect snapshot
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// doPublicModelsReq builds and serves a request against h.
func doPublicModelsReq(h *PublicModelsHandler, method string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/public/models", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func buildTestPublicHandler(t *testing.T) *PublicModelsHandler {
	t.Helper()
	startedAt := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC).Unix()
	snap := BuildPublicModelsSnapshot(startedAt)
	return NewPublicModelsHandler(silentLogger(), snap)
}

// Scenario: 4.7-UNIT-006
// Priority: P0  ·  Level: unit
//
// Method + header permutations. The route is OUTSIDE the bearer middleware
// chain, so a rogue Authorization header is silently ignored (the handler
// never consults the header). Non-GET/HEAD methods emit the canonical
// `405_method_not_allowed` envelope per OQ-4.7-6.
func Test_PublicModelsHandler_method_and_header_permutations(t *testing.T) {
	t.Parallel()
	h := buildTestPublicHandler(t)

	type tc struct {
		name       string
		method     string
		headers    map[string]string
		wantStatus int
	}
	cases := []tc{
		{name: "GET no auth", method: http.MethodGet, headers: nil, wantStatus: 200},
		{name: "GET rogue bearer ignored", method: http.MethodGet, headers: map[string]string{"Authorization": "Bearer he-anything"}, wantStatus: 200},
		{name: "POST returns 405", method: http.MethodPost, headers: nil, wantStatus: 405},
		{name: "PUT returns 405", method: http.MethodPut, headers: nil, wantStatus: 405},
		{name: "DELETE returns 405", method: http.MethodDelete, headers: nil, wantStatus: 405},
		{name: "PATCH returns 405", method: http.MethodPatch, headers: nil, wantStatus: 405},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rr := doPublicModelsReq(h, c.method, c.headers)
			if rr.Code != c.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rr.Code, c.wantStatus, rr.Body.String())
			}
			if c.wantStatus == http.StatusOK {
				// Valid body: 11-entry ModelList shape.
				var resp ModelsResponse
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatalf("body unmarshal: %v\nbody=%s", err, rr.Body.String())
				}
				if resp.Object != "list" {
					t.Errorf("object = %q, want \"list\"", resp.Object)
				}
				if len(resp.Data) != 14 {
					t.Errorf("len(data) = %d, want 14 (Story 9.6 — +doubao-asr)", len(resp.Data))
				}
				return
			}

			// 405 path — envelope + Allow: GET.
			if got := rr.Header().Get("Allow"); got != "GET" {
				t.Errorf("Allow header = %q, want \"GET\"", got)
			}
			var env map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
				t.Fatalf("405 envelope unmarshal: %v\nbody=%s", err, rr.Body.String())
			}
			errMap, _ := env["error"].(map[string]any)
			if errMap == nil {
				t.Fatalf("405 body missing error envelope: %s", rr.Body.String())
			}
			if got, _ := errMap["code"].(string); got != "405_method_not_allowed" {
				t.Errorf("error.code = %q, want \"405_method_not_allowed\"", got)
			}
			if got, _ := errMap["message"].(string); got != "Only GET is allowed on /public/models." {
				t.Errorf("error.message = %q", got)
			}
			if got, _ := errMap["type"].(string); got != "invalid_request_error" {
				t.Errorf("error.type = %q, want \"invalid_request_error\"", got)
			}
		})
	}
}

// Scenario: 4.7-UNIT-007
// Priority: P0  ·  Level: unit
//
// Public-endpoint slog MUST emit event=models_list_public and MUST NOT carry
// api_key_id (PII discipline; BR-1.9). The m-2 `remote_addr` field is
// allowed (originator IP is non-PII).
func Test_PublicModelsHandler_slog_omits_api_key_id(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC).Unix()
	snap := BuildPublicModelsSnapshot(startedAt)
	rh := &recordingHandler{}
	h := NewPublicModelsHandler(slog.New(rh), snap)

	rr := doPublicModelsReq(h, http.MethodGet, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := len(rh.records); got != 1 {
		t.Fatalf("len(records) = %d, want 1", got)
	}
	rec := rh.records[0]
	if rec.Message != "models_list_public" {
		t.Errorf("record.Message = %q, want \"models_list_public\"", rec.Message)
	}
	gotAttrs := map[string]any{}
	for _, a := range flatAttrs(rec) {
		gotAttrs[a.Key] = a.Value.Any()
	}
	if v, _ := gotAttrs["event"].(string); v != "models_list_public" {
		t.Errorf("attr event = %v, want \"models_list_public\"", gotAttrs["event"])
	}
	if v, ok := gotAttrs["api_key_id"]; ok {
		t.Errorf("attr api_key_id MUST NOT appear on public-endpoint slog; got %v", v)
	}
	if v, _ := gotAttrs["catalogue_size"].(int64); v != 14 {
		t.Errorf("attr catalogue_size = %v, want 14", gotAttrs["catalogue_size"])
	}
}

// Scenario: 4.7-BLIND-BOUNDARY-001
// Priority: P1  ·  Level: unit
//
// HEAD MUST behave as GET-with-suppressed-body (200 status; body may be
// emitted by the handler but is suppressed by Go's stdlib mux response
// flow in production — at the bare handler-call tier we accept either
// 200+empty body or 200+full body, but NOT 405).
func Test_PublicModelsHandler_HEAD_method(t *testing.T) {
	t.Parallel()
	h := buildTestPublicHandler(t)
	rr := doPublicModelsReq(h, http.MethodHead, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200 (HEAD treated as GET-with-no-body)", rr.Code)
	}
}

// Scenario: 4.7-BLIND-CONCURRENCY-001
// Priority: P1  ·  Level: integration
//
// 100 concurrent GETs against a single handler instance — every body MUST
// be byte-identical. Combined with `go test -race` this proves the
// snapshot is genuinely immutable across concurrent emits.
func Test_PublicModelsHandler_100_concurrent_byte_identical(t *testing.T) {
	t.Parallel()
	h := buildTestPublicHandler(t)
	const N = 100
	bodies := make([][]byte, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			rr := doPublicModelsReq(h, http.MethodGet, nil)
			bodies[idx] = append([]byte(nil), rr.Body.Bytes()...)
		}(i)
	}
	wg.Wait()
	for i := 1; i < N; i++ {
		if !bytes.Equal(bodies[0], bodies[i]) {
			t.Fatalf("body[%d] diverges from body[0]\n  body[0]=%s\n  body[%d]=%s",
				i, bodies[0], i, bodies[i])
		}
	}
}

// Scenario: 4.7-BLIND-CONCURRENCY-002
// Priority: P1  ·  Level: integration
//
// Cross-handler concurrent reads — 50 GETs against /v1/models + 50 GETs
// against /public/models all return byte-identical bodies (the snapshot
// is the single source of truth shared via constructor injection).
func Test_v1_and_public_models_concurrent_byte_identical(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, time.May, 20, 12, 0, 0, 0, time.UTC)
	bearer := NewModelsHandler(silentLogger(), WithModelsNow(fixedClock(startedAt)))
	publicH := NewPublicModelsHandler(silentLogger(), BuildPublicModelsSnapshot(startedAt.Unix()))

	const N = 50
	bearerBodies := make([][]byte, N)
	publicBodies := make([][]byte, N)
	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			rr := doModelsGet(bearer, modelsAuthedCtx(context.Background()))
			bearerBodies[idx] = append([]byte(nil), rr.Body.Bytes()...)
		}(i)
		go func(idx int) {
			defer wg.Done()
			rr := doPublicModelsReq(publicH, http.MethodGet, nil)
			publicBodies[idx] = append([]byte(nil), rr.Body.Bytes()...)
		}(i)
	}
	wg.Wait()

	want := bearerBodies[0]
	for i := 0; i < N; i++ {
		if !bytes.Equal(bearerBodies[i], want) {
			t.Errorf("bearer[%d] diverges from bearer[0]", i)
		}
		if !bytes.Equal(publicBodies[i], want) {
			t.Errorf("public[%d] diverges from bearer[0]\n  bearer[0]=%s\n  public[%d]=%s",
				i, want, i, publicBodies[i])
		}
	}
}

// Scenario: 4.7-BLIND-DATA-001
// Priority: P1  ·  Level: unit
//
// Per-request slice copy discipline — mutating a parsed response slice
// MUST NOT affect subsequent emits. The handler owns the canonical
// snapshot; clients see immutable views.
func Test_PublicModelsHandler_response_mutation_does_not_affect_snapshot(t *testing.T) {
	t.Parallel()
	h := buildTestPublicHandler(t)

	r1 := doPublicModelsReq(h, http.MethodGet, nil)
	var resp1 ModelsResponse
	if err := json.Unmarshal(r1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("r1 unmarshal: %v", err)
	}
	if len(resp1.Data) == 0 {
		t.Fatalf("r1 empty data")
	}
	originalChat := resp1.Data[0].Capabilities.Chat
	// Mutate the parsed slice — must not leak.
	resp1.Data[0].Capabilities.Chat = !originalChat
	resp1.Data[0].ID = "MUTATED"

	r2 := doPublicModelsReq(h, http.MethodGet, nil)
	var resp2 ModelsResponse
	if err := json.Unmarshal(r2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("r2 unmarshal: %v", err)
	}
	if resp2.Data[0].ID == "MUTATED" {
		t.Errorf("snapshot mutation leaked: data[0].id = %q", resp2.Data[0].ID)
	}
	if resp2.Data[0].Capabilities.Chat != originalChat {
		t.Errorf("snapshot mutation leaked: data[0].capabilities.chat = %v, want %v",
			resp2.Data[0].Capabilities.Chat, originalChat)
	}
}

// Sanity test — guards against a future refactor accidentally exporting
// or removing the constructor's `nil snapshot` tolerance.
func Test_NewPublicModelsHandler_handles_empty_snapshot(t *testing.T) {
	t.Parallel()
	h := NewPublicModelsHandler(silentLogger(), nil)
	rr := doPublicModelsReq(h, http.MethodGet, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty snapshot — must still 200)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"data":[]`) && !strings.Contains(rr.Body.String(), `"data":null`) {
		t.Errorf("empty-snapshot body = %s; want either data:[] or data:null", rr.Body.String())
	}
}

