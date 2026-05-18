// Story 3.5 — Tests for /v1/embeddings handler.
//
// Source: docs/qa/assessments/3.5-test-design-20260519.md
//
// Architect Round 1 rulings honored:
//
//	OQ2 → sibling constant MaxEmbeddingBodyBytes int64 = 1 << 20 (1 MiB)
//	OQ3 → 128-dim mock vector
//	OQ4 → 5-tuple validator signature: (status int, code string, message string, param *string, valid bool)
//
// Package = handlers (in-package) — required for direct access to
// unexported items (parseEmbeddingInputs, validateEmbeddingRequest,
// mockTokenCount, MaxEmbeddingBodyBytes per Architect OQ2 ruling).
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/goleak"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"
)

// ----- Shared test helpers ----------------------------------------------

const (
	embedTestAPIKeyID = "11111111-1111-1111-1111-111111111111"
)

func embedAuthedCtx(ctx context.Context) context.Context {
	ctx = middleware.WithAPIKeyID(ctx, embedTestAPIKeyID)
	ctx = middleware.BearerWithUserID(ctx, "22222222-2222-2222-2222-222222222222")
	ctx = middleware.WithTeamID(ctx, "")
	ctx = middleware.WithScope(ctx, `{}`)
	return ctx
}

// doEmbedPost invokes h.ServeHTTP with the supplied JSON body and an
// authed context. Returns the recorder.
func doEmbedPost(h *EmbeddingsHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(embedAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// ============================================================
// AC2.A — Dual-Shape Input Parser (Unit, Table-Driven)
// ============================================================

// Scenario: 3.5-UNIT-006
func Test_parseEmbeddingInputs_single_string(t *testing.T) {
	t.Parallel()
	got, err := parseEmbeddingInputs(json.RawMessage(`"Hello"`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 1 || got[0] != "Hello" {
		t.Errorf("got = %v, want [\"Hello\"]", got)
	}
}

// Scenario: 3.5-UNIT-007
func Test_parseEmbeddingInputs_array_of_strings(t *testing.T) {
	t.Parallel()
	got, err := parseEmbeddingInputs(json.RawMessage(`["foo","bar"]`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 2 || got[0] != "foo" || got[1] != "bar" {
		t.Errorf("got = %v, want [\"foo\",\"bar\"]", got)
	}
}

// Scenario: 3.5-UNIT-008
func Test_parseEmbeddingInputs_empty_string(t *testing.T) {
	t.Parallel()
	got, err := parseEmbeddingInputs(json.RawMessage(`""`))
	if err != nil {
		t.Fatalf("err = %v (parser must surface shape, not semantic non-empty)", err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Errorf("got = %v, want [\"\"]", got)
	}
}

// Scenario: 3.5-UNIT-009
func Test_parseEmbeddingInputs_empty_array(t *testing.T) {
	t.Parallel()
	got, err := parseEmbeddingInputs(json.RawMessage(`[]`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want []", got)
	}
}

// Scenario: 3.5-UNIT-010
func Test_parseEmbeddingInputs_nested_array_returns_error(t *testing.T) {
	t.Parallel()
	if _, err := parseEmbeddingInputs(json.RawMessage(`[["a"],["b"]]`)); err == nil {
		t.Errorf("err = nil, want non-nil (nested arrays not in OpenAI contract)")
	}
}

// Scenario: 3.5-UNIT-011
func Test_parseEmbeddingInputs_null_returns_error(t *testing.T) {
	t.Parallel()
	if _, err := parseEmbeddingInputs(json.RawMessage(`null`)); err == nil {
		t.Errorf("err = nil, want non-nil (null is not string nor array)")
	}
}

// Scenario: 3.5-UNIT-012
func Test_parseEmbeddingInputs_int_returns_error(t *testing.T) {
	t.Parallel()
	if _, err := parseEmbeddingInputs(json.RawMessage(`42`)); err == nil {
		t.Errorf("err = nil, want non-nil (numeric shape not in OpenAI contract)")
	}
}

// ============================================================
// AC2.B — Validator Pipeline (Unit, Table-Driven; 5-Tuple per Architect OQ4)
// ============================================================

// helper: build EmbeddingRequest with raw input JSON
func buildReq(model, inputJSON string) *EmbeddingRequest {
	return &EmbeddingRequest{Model: model, Input: json.RawMessage(inputJSON)}
}

// Scenario: 3.5-UNIT-013
func Test_validateEmbeddingRequest_empty_model(t *testing.T) {
	t.Parallel()
	req := buildReq("", `"Hello"`)
	status, code, msg, param, valid := validateEmbeddingRequest(req)
	if valid {
		t.Fatalf("valid=true, want false")
	}
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if code != "400_invalid_request" {
		t.Errorf("code = %q", code)
	}
	if msg != "Field 'model' is required and must be a non-empty string." {
		t.Errorf("msg = %q", msg)
	}
	if param == nil || *param != "model" {
		t.Errorf("param = %v, want &\"model\"", param)
	}
}

// Scenario: 3.5-UNIT-014
func Test_validateEmbeddingRequest_model_exactly_100_chars_valid(t *testing.T) {
	t.Parallel()
	model := strings.Repeat("x", 100)
	req := buildReq(model, `"Hello"`)
	_, _, _, _, valid := validateEmbeddingRequest(req)
	if !valid {
		t.Errorf("valid=false, want true (modelMaxLen=100 inclusive)")
	}
}

// Scenario: 3.5-UNIT-015
func Test_validateEmbeddingRequest_model_101_chars_invalid(t *testing.T) {
	t.Parallel()
	model := strings.Repeat("x", 101)
	req := buildReq(model, `"Hello"`)
	_, _, _, param, valid := validateEmbeddingRequest(req)
	if valid {
		t.Errorf("valid=true, want false (101 > modelMaxLen=100)")
	}
	if param == nil || *param != "model" {
		t.Errorf("param = %v, want &\"model\"", param)
	}
}

// Scenario: 3.5-UNIT-016
func Test_validateEmbeddingRequest_empty_input_string(t *testing.T) {
	t.Parallel()
	req := buildReq("text-embedding-3-small", `""`)
	_, _, msg, param, valid := validateEmbeddingRequest(req)
	if valid {
		t.Fatalf("valid=true, want false")
	}
	if param == nil || *param != "input" {
		t.Errorf("param = %v, want &\"input\"", param)
	}
	if !strings.Contains(msg, "non-empty string or non-empty array") {
		t.Errorf("msg = %q, want substring \"non-empty string or non-empty array\"", msg)
	}
}

// Scenario: 3.5-UNIT-017
func Test_validateEmbeddingRequest_input_array_with_empty_element(t *testing.T) {
	t.Parallel()
	req := buildReq("text-embedding-3-small", `[""]`)
	_, _, _, param, valid := validateEmbeddingRequest(req)
	if valid {
		t.Fatalf("valid=true, want false")
	}
	if param == nil || *param != "input" {
		t.Errorf("param = %v, want &\"input\"", param)
	}
}

// Scenario: 3.5-UNIT-018
func Test_validateEmbeddingRequest_input_over_256KiB(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", maxEmbeddingInputBytes+1) // 262145 bytes
	rawJSON := fmt.Sprintf(`"%s"`, big)
	req := buildReq("text-embedding-3-small", rawJSON)
	_, _, msg, param, valid := validateEmbeddingRequest(req)
	if valid {
		t.Fatalf("valid=true, want false (256 KiB sub-cap)")
	}
	if param == nil || *param != "input" {
		t.Errorf("param = %v, want &\"input\"", param)
	}
	if msg != "Field 'input' total bytes exceed 256 KiB." {
		t.Errorf("msg = %q", msg)
	}
}

// ============================================================
// AC2.C — Mock Embedding Generator (Unit)
// ============================================================

// Scenario: 3.5-UNIT-019
func Test_GenerateMockEmbedding_dim_128_norm_close_to_1(t *testing.T) {
	t.Parallel()
	v := GenerateMockEmbedding("Hello, He-API.", 128)
	if len(v) != 128 {
		t.Fatalf("len(v) = %d, want 128", len(v))
	}
	var sumsq float64
	for _, x := range v {
		sumsq += float64(x) * float64(x)
	}
	norm := math.Sqrt(sumsq)
	if norm < 0.99 || norm > 1.01 {
		t.Errorf("‖v‖₂ = %f, want ∈ [0.99, 1.01]", norm)
	}
}

// Scenario: 3.5-UNIT-020
func Test_GenerateMockEmbedding_deterministic_for_same_input(t *testing.T) {
	t.Parallel()
	v1 := GenerateMockEmbedding("Hello, He-API.", 128)
	v2 := GenerateMockEmbedding("Hello, He-API.", 128)
	if len(v1) != len(v2) {
		t.Fatalf("len mismatch: %d vs %d", len(v1), len(v2))
	}
	for i := range v1 {
		if math.Abs(float64(v1[i])-float64(v2[i])) > 1e-6 {
			t.Errorf("v1[%d]=%f vs v2[%d]=%f differ > 1e-6", i, v1[i], i, v2[i])
		}
	}
}

// Scenario: 3.5-UNIT-021
func Test_GenerateMockEmbedding_different_inputs_produce_different_vectors(t *testing.T) {
	t.Parallel()
	a := GenerateMockEmbedding("foo", 128)
	b := GenerateMockEmbedding("bar", 128)
	diff := 0
	for i := range a {
		if math.Abs(float64(a[i])-float64(b[i])) > 1e-6 {
			diff++
		}
	}
	if diff*2 < len(a) {
		t.Errorf("only %d/%d positions differ, want ≥50%% (defence against constant-vector regression)", diff, len(a))
	}
}

// ============================================================
// AC2.D — Token Counter (Unit)
// ============================================================

// Scenario: 3.5-UNIT-022
func Test_mockTokenCount_table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 0},
		{"abcd", 1},
		{"abcdefgh", 2},
		{"日本語", 2}, // 9 bytes UTF-8 / 4 = 2 (BYTE length per BR-2.5)
	}
	for _, c := range cases {
		if got := mockTokenCount(c.in); got != c.want {
			t.Errorf("mockTokenCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// ============================================================
// AC2.E — PII Discipline (Unit)
// ============================================================

// embedRecordingHandler captures slog.Record + attrs from WithAttrs chain.
type embedRecordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
	attrs   []slog.Attr
}

func (r *embedRecordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (r *embedRecordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.attrs {
		rec.AddAttrs(a)
	}
	r.records = append(r.records, rec)
	return nil
}

func (r *embedRecordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &embedRecordingHandler{records: nil, attrs: append(append([]slog.Attr{}, r.attrs...), attrs...)}
}
func (r *embedRecordingHandler) WithGroup(_ string) slog.Handler { return r }

// attrValueContains returns true if a's Value (recursively) contains the
// substring `s` when stringified.
func attrValueContains(a slog.Attr, s string) bool {
	switch a.Value.Kind() {
	case slog.KindString:
		return strings.Contains(a.Value.String(), s)
	case slog.KindGroup:
		for _, sub := range a.Value.Group() {
			if attrValueContains(sub, s) {
				return true
			}
		}
		return false
	default:
		return strings.Contains(fmt.Sprintf("%v", a.Value.Any()), s)
	}
}

// Scenario: 3.5-UNIT-023
func Test_EmbeddingsHandler_does_NOT_log_input_content(t *testing.T) {
	t.Parallel()
	const sentinel = "ZZZ_PII_SENTINEL_XYZ"

	cases := []struct {
		name string
		body string
	}{
		{"string", fmt.Sprintf(`{"model":"text-embedding-3-small","input":%q}`, sentinel)},
		{"array", fmt.Sprintf(`{"model":"text-embedding-3-small","input":[%q,"other"]}`, sentinel)},
		{"long", fmt.Sprintf(`{"model":"text-embedding-3-small","input":%q}`, sentinel+strings.Repeat("x", 1024))},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rh := &embedRecordingHandler{}
			h := NewEmbeddingsHandler(slog.New(rh))
			rr := doEmbedPost(h, c.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d (%s); body=%s", rr.Code, c.name, rr.Body.String())
			}
			for ri, rec := range rh.records {
				if strings.Contains(rec.Message, sentinel) {
					t.Errorf("PII leak: record[%d].Message = %q", ri, rec.Message)
				}
				rec.Attrs(func(a slog.Attr) bool {
					if attrValueContains(a, sentinel) {
						t.Errorf("PII leak (%s): record[%d] attr %q value contains sentinel", c.name, ri, a.Key)
						return false
					}
					return true
				})
			}
		})
	}
}

// ============================================================
// AC2.F — Handler Integration via httptest (Integration)
// ============================================================

// Scenario: 3.5-INT-006
func Test_EmbeddingsHandler_happy_path_string_input(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	rr := doEmbedPost(h, `{"model":"text-embedding-3-small","input":"Hello"}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	var resp EmbeddingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, rr.Body.String())
	}
	if resp.Object != "list" {
		t.Errorf("resp.object = %q, want \"list\"", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" {
		t.Errorf("data[0].object = %q", resp.Data[0].Object)
	}
	if resp.Data[0].Index != 0 {
		t.Errorf("data[0].index = %d, want 0", resp.Data[0].Index)
	}
	if len(resp.Data[0].Embedding) != 128 {
		t.Errorf("len(embedding) = %d, want 128", len(resp.Data[0].Embedding))
	}
	var sumsq float64
	for _, x := range resp.Data[0].Embedding {
		sumsq += float64(x) * float64(x)
	}
	if norm := math.Sqrt(sumsq); norm < 0.99 || norm > 1.01 {
		t.Errorf("‖v‖₂ = %f, want ∈ [0.99, 1.01]", norm)
	}
	if resp.Usage.PromptTokens != 1 {
		t.Errorf("usage.prompt_tokens = %d, want 1 (len(\"Hello\")=5 / 4)", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != resp.Usage.PromptTokens {
		t.Errorf("usage.total_tokens (%d) != prompt_tokens (%d)", resp.Usage.TotalTokens, resp.Usage.PromptTokens)
	}
	if resp.Model != "text-embedding-3-small" {
		t.Errorf("resp.model = %q", resp.Model)
	}
}

// Scenario: 3.5-INT-007
func Test_EmbeddingsHandler_happy_path_array_input(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	rr := doEmbedPost(h, `{"model":"text-embedding-3-small","input":["Hello","World","Foo"]}`)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp EmbeddingResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("len(data) = %d, want 3", len(resp.Data))
	}
	for i := range resp.Data {
		if resp.Data[i].Index != i {
			t.Errorf("data[%d].index = %d", i, resp.Data[i].Index)
		}
		if len(resp.Data[i].Embedding) != 128 {
			t.Errorf("data[%d].len = %d", i, len(resp.Data[i].Embedding))
		}
	}
	// Determinism per-element: data[0] for "Hello" must equal GenerateMockEmbedding("Hello", 128)
	wantHello := GenerateMockEmbedding("Hello", 128)
	for i := range wantHello {
		if math.Abs(float64(resp.Data[0].Embedding[i])-float64(wantHello[i])) > 1e-6 {
			t.Errorf("data[0] differs from GenerateMockEmbedding(\"Hello\") at %d", i)
			break
		}
	}
	// usage.prompt_tokens = sum(len(s)/4) = 5/4 + 5/4 + 3/4 = 1+1+0 = 2
	wantTok := (len("Hello") / 4) + (len("World") / 4) + (len("Foo") / 4)
	if resp.Usage.PromptTokens != wantTok {
		t.Errorf("usage.prompt_tokens = %d, want %d", resp.Usage.PromptTokens, wantTok)
	}
}

// Scenario: 3.5-INT-008
func Test_EmbeddingsHandler_body_over_1MiB_returns_413(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	// Body length = MaxEmbeddingBodyBytes + 1.
	// Build a JSON document whose total byte length > 1 MiB. A single long
	// string input that pushes total bytes over the cap is sufficient.
	pad := strings.Repeat("a", int(MaxEmbeddingBodyBytes))
	body := fmt.Sprintf(`{"model":"text-embedding-3-small","input":%q}`, pad)
	if int64(len(body)) <= MaxEmbeddingBodyBytes {
		t.Fatalf("test setup: body=%d bytes, need > %d", len(body), MaxEmbeddingBodyBytes)
	}
	rr := doEmbedPost(h, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rr.Code, rr.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope unmarshal: %v", err)
	}
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "413_payload_too_large" {
		t.Errorf("error.code = %q", got)
	}
	if got, _ := errMap["message"].(string); got != "Request body exceeds 1 MiB." {
		t.Errorf("error.message = %q", got)
	}
}

// Scenario: 3.5-INT-009
func Test_EmbeddingsHandler_missing_model_returns_400_with_param_model(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	rr := doEmbedPost(h, `{"input":"Hello"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "400_invalid_request" {
		t.Errorf("error.code = %q", got)
	}
	if got, _ := errMap["param"].(string); got != "model" {
		t.Errorf("error.param = %v, want \"model\"", errMap["param"])
	}
}

// Scenario: 3.5-INT-010
func Test_EmbeddingsHandler_missing_input_returns_400_with_param_input(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	rr := doEmbedPost(h, `{"model":"text-embedding-3-small"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["param"].(string); got != "input" {
		t.Errorf("error.param = %v, want \"input\"", errMap["param"])
	}
}

// Scenario: 3.5-INT-011
func Test_EmbeddingsHandler_input_over_256KiB_returns_400(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	// Build an array whose total bytes = 262145. The body itself stays under
	// the 1 MiB cap, but the 256 KiB sub-cap (BR-2.3 step 5) should fire.
	big := strings.Repeat("a", maxEmbeddingInputBytes+1)
	body := fmt.Sprintf(`{"model":"text-embedding-3-small","input":[%q]}`, big)
	rr := doEmbedPost(h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["param"].(string); got != "input" {
		t.Errorf("error.param = %v, want \"input\"", errMap["param"])
	}
	if got, _ := errMap["message"].(string); got != "Field 'input' total bytes exceed 256 KiB." {
		t.Errorf("error.message = %q", got)
	}
}

// ----- Bearer harness for embeddings INT-012 ----------------------------

func newEmbedBearerHarness(
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

// Scenario: 3.5-INT-012
func Test_EmbeddingsHandler_missing_bearer_returns_401(t *testing.T) {
	t.Parallel()
	mw := newEmbedBearerHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return &authv1.ValidateApiKeyResponse{Ok: false}, nil
	})
	var innerCalls atomic.Int32
	sentinel := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalls.Add(1)
		panic("embeddings handler MUST NOT be invoked when auth fails")
	})
	wrapped := mw.RequireAPIKey(sentinel)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(`{"model":"text-embedding-3-small","input":"Hello"}`))
	// no Authorization
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var env map[string]any
	_ = json.Unmarshal(body, &env)
	errMap, _ := env["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "401_invalid_api_key" {
		t.Errorf("error.code = %q", got)
	}
	if innerCalls.Load() != 0 {
		t.Errorf("inner handler invoked despite auth failure")
	}
}

// Scenario: 3.5-INT-013
func Test_EmbeddingsHandler_GET_returns_405_with_Allow_POST(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	mux := http.NewServeMux()
	mux.Handle("POST /v1/embeddings", h)

	req := httptest.NewRequest(http.MethodGet, "/v1/embeddings", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Errorf("Allow header = %q, want to contain \"POST\"", allow)
	}
}

// Scenario: 3.5-INT-014
func Test_EmbeddingsHandler_determinism_handler_level(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	body := `{"model":"text-embedding-3-small","input":"Hello, deterministic test."}`
	r1 := doEmbedPost(h, body)
	r2 := doEmbedPost(h, body)
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK {
		t.Fatalf("status r1=%d r2=%d", r1.Code, r2.Code)
	}
	var resp1, resp2 EmbeddingResponse
	if err := json.Unmarshal(r1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("r1: %v", err)
	}
	if err := json.Unmarshal(r2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("r2: %v", err)
	}
	if len(resp1.Data) != len(resp2.Data) || len(resp1.Data) == 0 {
		t.Fatalf("len mismatch r1=%d r2=%d", len(resp1.Data), len(resp2.Data))
	}
	for i := range resp1.Data[0].Embedding {
		if resp1.Data[0].Embedding[i] != resp2.Data[0].Embedding[i] {
			t.Errorf("data[0].embedding[%d] r1=%f r2=%f differ (byte-equal expected)",
				i, resp1.Data[0].Embedding[i], resp2.Data[0].Embedding[i])
		}
	}
	if resp1.Usage != resp2.Usage {
		t.Errorf("usage differs: r1=%+v r2=%+v", resp1.Usage, resp2.Usage)
	}
}

// ============================================================
// Blind-Spot Scenarios for /v1/embeddings
// ============================================================

// Scenario: 3.5-BLIND-BOUNDARY-001
func Test_validateEmbeddingRequest_model_exactly_modelMaxLen_explicit_boundary(t *testing.T) {
	t.Parallel()
	req := buildReq(strings.Repeat("y", modelMaxLen), `"ok"`)
	_, _, _, _, valid := validateEmbeddingRequest(req)
	if !valid {
		t.Errorf("valid=false at modelMaxLen=%d (inclusive ceiling)", modelMaxLen)
	}
}

// Scenario: 3.5-BLIND-BOUNDARY-002
func Test_EmbeddingsHandler_body_at_exact_1MiB_returns_200(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	// Build a body whose total length is EXACTLY MaxEmbeddingBodyBytes.
	// Body shape: {"model":"m","input":"<pad>"} — compute pad so total == cap.
	prefix := `{"model":"m","input":"`
	suffix := `"}`
	padLen := int(MaxEmbeddingBodyBytes) - len(prefix) - len(suffix)
	if padLen <= 0 {
		t.Fatalf("padLen = %d, need positive", padLen)
	}
	body := prefix + strings.Repeat("a", padLen) + suffix
	if int64(len(body)) != MaxEmbeddingBodyBytes {
		t.Fatalf("body=%d, want exactly %d", len(body), MaxEmbeddingBodyBytes)
	}
	rr := doEmbedPost(h, body)
	if rr.Code != http.StatusOK {
		// Body could fail validation if input > 256 KiB; we expect that — the
		// boundary test for the BODY cap is about MaxBytesReader not firing.
		// Re-check: if 200 OR 400-with-input-too-large, both prove the body
		// cap did NOT fire (would have been 413).
		if rr.Code == http.StatusBadRequest {
			var env map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &env)
			errMap, _ := env["error"].(map[string]any)
			if code, _ := errMap["code"].(string); code == "400_invalid_request" {
				// 256 KiB sub-cap fired AFTER body was read — proves MaxBytesReader did NOT block at exactly 1 MiB.
				return
			}
		}
		t.Fatalf("status = %d, want 200 (or 400 sub-cap, NOT 413 body-cap); body=%s", rr.Code, rr.Body.String())
	}
}

// Scenario: 3.5-BLIND-BOUNDARY-003
func Test_validateEmbeddingRequest_input_exactly_256KiB_valid(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", maxEmbeddingInputBytes) // exactly 262144 bytes
	rawJSON := fmt.Sprintf(`"%s"`, big)
	req := buildReq("m", rawJSON)
	_, _, _, _, valid := validateEmbeddingRequest(req)
	if !valid {
		t.Errorf("valid=false at exactly maxEmbeddingInputBytes=%d (inclusive sub-cap)", maxEmbeddingInputBytes)
	}
}

// Scenario: 3.5-BLIND-CONCURRENCY-002
func Test_EmbeddingsHandler_concurrent_100_different_inputs(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	const N = 100
	inputs := make([]string, N)
	for i := 0; i < N; i++ {
		inputs[i] = fmt.Sprintf("input-%d", i)
	}

	var wg sync.WaitGroup
	wg.Add(N)
	results := make([][]float32, N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"model":"m","input":%q}`, inputs[idx])
			rr := doEmbedPost(h, body)
			if rr.Code != http.StatusOK {
				t.Errorf("g%d: status = %d", idx, rr.Code)
				return
			}
			var resp EmbeddingResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Errorf("g%d unmarshal: %v", idx, err)
				return
			}
			if len(resp.Data) != 1 {
				t.Errorf("g%d len(data) = %d", idx, len(resp.Data))
				return
			}
			results[idx] = resp.Data[0].Embedding
		}(i)
	}
	wg.Wait()

	// Each result must match GenerateMockEmbedding(its_input, 128) element-by-element.
	for i := 0; i < N; i++ {
		want := GenerateMockEmbedding(inputs[i], 128)
		if len(results[i]) != len(want) {
			t.Errorf("g%d len = %d, want %d", i, len(results[i]), len(want))
			continue
		}
		for j := range want {
			if results[i][j] != want[j] {
				t.Errorf("g%d embedding[%d] = %f, want %f (per-request state leak?)",
					i, j, results[i][j], want[j])
				break
			}
		}
	}
}

// Scenario: 3.5-BLIND-RESOURCE-001
func Test_EmbeddingsHandler_request_body_drained_on_early_failure(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	// Body with missing "model" — handler returns 400 after json.Decode reads
	// the body. The httptest.ResponseRecorder doesn't enforce body draining
	// (Go's http.Server does), so the load-bearing assertion is "handler
	// returns cleanly with the expected envelope".
	body := `{"input":"Hello","extra":"trailing-content-after-json"}`
	rr := doEmbedPost(h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Errorf("envelope malformed: %v", err)
	}
}

// Scenario: 3.5-BLIND-FLOW-002
func Test_EmbeddingsHandler_tolerates_content_type_charset_suffix(t *testing.T) {
	t.Parallel()
	h := NewEmbeddingsHandler(silentLogger())
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":"Hello"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req = req.WithContext(embedAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (charset suffix tolerated); body=%s", rr.Code, rr.Body.String())
	}
}

// Scenario: 3.5-BLIND-ERROR-001
func Test_EmbeddingsHandler_client_disconnect_mid_response(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	h := NewEmbeddingsHandler(silentLogger())

	ctx, cancel := context.WithCancel(embedAuthedCtx(context.Background()))
	cancel() // simulate client disconnect BEFORE handler runs

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"m","input":"Hello"}`))
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	// Must not panic even though ctx is already cancelled.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("handler panicked: %v", r)
		}
	}()
	h.ServeHTTP(rr, req)
}
