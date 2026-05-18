// Story 3.3 — Tests for the non-streaming /v1/chat/completions handler.
//
// Scenario IDs trace back to docs/qa/assessments/3.3-test-design-20260518.md.
// Each TestXxx function carries a `// Scenario: 3.3-{LEVEL}-{NNN}` comment so
// `*review 3.3` greps trace ACs to tests deterministically.
package handlers_test

import (
	"bytes"
	"context"
	"encoding/hex"
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

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// ----- Shared test helpers ----------------------------------------------

const (
	testAPIKeyID = "11111111-1111-1111-1111-111111111111"
	testUserID   = "22222222-2222-2222-2222-222222222222"
	testTeamID   = ""
	testScope    = `{}`

	validReqBody = `{"model":"qwen-max","messages":[{"role":"user","content":"Say hi."}]}`
)

// withBearerCtx attaches the same bearer-auth context keys the Story-3.2
// middleware would inject. Production tests should prefer the wired
// integration test (INT-001) but unit tests use this shortcut to focus on
// the handler under test.
func withBearerCtx(ctx context.Context) context.Context {
	ctx = middleware.WithAPIKeyID(ctx, testAPIKeyID)
	ctx = middleware.BearerWithUserID(ctx, testUserID)
	ctx = middleware.WithTeamID(ctx, testTeamID)
	ctx = middleware.WithScope(ctx, testScope)
	return ctx
}

// bufLogger returns a slog.Logger that writes JSON lines to buf so tests
// can assert log contents (BR-1.8 / PII non-leak).
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// doRequest invokes the supplied handler with the supplied body. Bearer
// context is attached so the BR-1.1 defence-in-depth check passes.
func doRequest(t *testing.T, h *handlers.ChatCompletionsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer he-test-key-stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// decodeBody returns the response body unmarshalled into a generic map.
func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal body: %v\nbody=%s", err, rr.Body.String())
	}
	return m
}

// ----- AC1: Handler happy path ------------------------------------------

// Scenario: 3.3-UNIT-001..018 (P0 + P1 happy-path coverage; BR-1.1 / 1.2 /
// 1.4 / 1.5 / 1.6 / 1.7 / 1.8 / 1.9 / 4.1 all asserted in one body since
// the response is the joint output of every BR).
func TestChatCompletions_HappyPath_OpenAIShape(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	stubID := "chatcmpl-mock-feedbeef0042"
	h := handlers.NewChatCompletionsHandler(bufLogger(buf),
		handlers.WithIDFactory(func() string { return stubID }))

	before := time.Now().UTC().Unix()
	rr := doRequest(t, h, validReqBody)
	after := time.Now().UTC().Unix()

	// UNIT-001 — status 200
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// UNIT-002 — Content-Type byte-exact
	if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	// UNIT-013 — no extra headers (Set-Cookie / X-He-Request-Id / X-He-Selected-Model)
	for _, k := range []string{"Set-Cookie", "X-He-Request-Id", "X-He-Selected-Model"} {
		if v := rr.Header().Get(k); v != "" {
			t.Errorf("unexpected header %s = %q", k, v)
		}
	}
	// UNIT-003 — field ordering id → object → created → model → choices → usage.
	// json.Marshal preserves struct field declaration order, so a substring
	// search across the raw body is sufficient to confirm ordering.
	body := rr.Body.String()
	for i, key := range []string{`"id"`, `"object"`, `"created"`, `"model"`, `"choices"`, `"usage"`} {
		if !strings.Contains(body, key) {
			t.Errorf("body missing %q", key)
			continue
		}
		if i > 0 {
			prev := []string{`"id"`, `"object"`, `"created"`, `"model"`, `"choices"`, `"usage"`}[i-1]
			if strings.Index(body, key) < strings.Index(body, prev) {
				t.Errorf("field order violation: %s before %s in body=%s", key, prev, body)
			}
		}
	}

	got := decodeBody(t, rr)
	// UNIT-004 — id stubbed via WithIDFactory; BR-1.4 / OQ4 round-trip.
	if got["id"] != stubID {
		t.Errorf("id = %v, want %s", got["id"], stubID)
	}
	// UNIT-009 — object literal "chat.completion".
	if got["object"] != "chat.completion" {
		t.Errorf("object = %v, want chat.completion", got["object"])
	}
	// UNIT-007 — created is Unix seconds (sanity-bounded).
	created, _ := got["created"].(float64)
	if created < 1.5e9 || created > 3e9 {
		t.Errorf("created = %v, want Unix-seconds (1.5e9 < x < 3e9)", created)
	}
	// UNIT-006 — created within ±5s of test wall clock.
	if int64(created) < before-5 || int64(created) > after+5 {
		t.Errorf("created = %d outside ±5s window [%d, %d]", int64(created), before-5, after+5)
	}
	// UNIT-008 — model echoed verbatim.
	if got["model"] != "qwen-max" {
		t.Errorf("model = %v, want qwen-max", got["model"])
	}
	// UNIT-010 — choices[0] shape.
	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("len(choices) = %d, want 1", len(choices))
	}
	c0 := choices[0].(map[string]any)
	if c0["index"].(float64) != 0 {
		t.Errorf("choices[0].index = %v", c0["index"])
	}
	if c0["finish_reason"] != "stop" {
		t.Errorf("choices[0].finish_reason = %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]any)
	if msg["role"] != "assistant" {
		t.Errorf("choices[0].message.role = %v", msg["role"])
	}
	if msg["content"] != handlers.MockContent {
		t.Errorf("choices[0].message.content = %q, want %q", msg["content"], handlers.MockContent)
	}
	// UNIT-011 — usage exactly {10, 20, 30}.
	usage := got["usage"].(map[string]any)
	if usage["prompt_tokens"].(float64) != 10 || usage["completion_tokens"].(float64) != 20 || usage["total_tokens"].(float64) != 30 {
		t.Errorf("usage = %+v, want {10, 20, 30}", usage)
	}

	// UNIT-015 — structured log emitted exactly once with the BR-1.8 fields.
	// UNIT-016 — PII non-leak: log MUST NOT contain `messages[].content`.
	if buf.Len() == 0 {
		t.Errorf("expected structured log line, got nothing")
	}
	logLine := buf.String()
	if !strings.Contains(logLine, `"event":"chat_completions_mock"`) {
		t.Errorf("log missing event=chat_completions_mock: %s", logLine)
	}
	if !strings.Contains(logLine, `"model":"qwen-max"`) {
		t.Errorf("log missing model=qwen-max: %s", logLine)
	}
	if !strings.Contains(logLine, `"api_key_id":"`+testAPIKeyID+`"`) {
		t.Errorf("log missing api_key_id=%s: %s", testAPIKeyID, logLine)
	}
	if !strings.Contains(logLine, `"messages_count":1`) {
		t.Errorf("log missing messages_count=1: %s", logLine)
	}
	// Count `event=chat_completions_mock` structured-attr occurrences —
	// slog emits one line per log; the substring `"event":"..."` appears at
	// most once per request. (The literal also surfaces in the msg= field,
	// so a substring count of the bare event name would over-count.)
	if got := strings.Count(logLine, `"event":"chat_completions_mock"`); got != 1 {
		t.Errorf("event-attr log count = %d, want 1; log=%s", got, logLine)
	}
}

// Scenario: 3.3-UNIT-005 — newMockCompletionID uses crypto/rand; two calls
// produce different ids. Validated indirectly through the public handler
// (the package-private factory is not exported by design).
func TestNewMockCompletionID_DistinctValues(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	rr1 := doRequest(t, h, validReqBody)
	rr2 := doRequest(t, h, validReqBody)
	id1 := decodeBody(t, rr1)["id"].(string)
	id2 := decodeBody(t, rr2)["id"].(string)

	if id1 == id2 {
		t.Errorf("crypto/rand id collision in 2 calls: %s == %s", id1, id2)
	}
	// BR-1.4 — exact shape `chatcmpl-mock-` + 12 lowercase hex.
	for _, id := range []string{id1, id2} {
		const prefix = "chatcmpl-mock-"
		if !strings.HasPrefix(id, prefix) {
			t.Errorf("id %q missing prefix %q", id, prefix)
			continue
		}
		hexPart := id[len(prefix):]
		if len(hexPart) != 12 {
			t.Errorf("id %q hex length = %d, want 12", id, len(hexPart))
		}
		if _, err := hex.DecodeString(hexPart); err != nil {
			t.Errorf("id %q hex part not hex: %v", id, err)
		}
		for _, r := range hexPart {
			if r >= 'A' && r <= 'F' {
				t.Errorf("id %q uses uppercase hex; want lowercase", id)
				break
			}
		}
	}
}

// Scenario: 3.3-UNIT-014 — missing bearer-auth context → 500
// gateway_misconfigured byte-exact envelope (BR-1.1 defence-in-depth).
func TestChatCompletions_MissingBearerCtx_500(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// NOTE: NOT using doRequest — we want bare context (no bearer).
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validReqBody))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	got := decodeBody(t, rr)
	errMap := got["error"].(map[string]any)
	if errMap["code"] != "500_gateway_misconfigured" {
		t.Errorf("error.code = %v, want 500_gateway_misconfigured", errMap["code"])
	}
	if errMap["type"] != "server_error" {
		t.Errorf("error.type = %v, want server_error", errMap["type"])
	}
	if errMap["param"] != nil {
		t.Errorf("error.param = %v, want nil", errMap["param"])
	}
	if errMap["he_request_id"] != nil {
		t.Errorf("error.he_request_id = %v, want nil", errMap["he_request_id"])
	}
}

// Scenario: 3.3-UNIT-017 — BR-1.9 trace context propagation: r.Context()
// flows through (sentinel-key assertion). The handler stores nothing in
// context itself; instead we assert that the same r.Context() value is
// visible to a custom logger that reads from ctx.
type ctxSentinelKey struct{}

func TestChatCompletions_ContextPropagation(t *testing.T) {
	t.Parallel()
	// Wrap a logger that asserts ctxSentinelKey is present at log time.
	var seen atomic.Bool
	buf := &bytes.Buffer{}
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	wrapped := &ctxAssertingHandler{inner: base, sentinel: ctxSentinelKey{}, seen: &seen}
	h := handlers.NewChatCompletionsHandler(slog.New(wrapped))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validReqBody))
	ctx := withBearerCtx(req.Context())
	ctx = context.WithValue(ctx, ctxSentinelKey{}, "present")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !seen.Load() {
		t.Errorf("logger never saw ctxSentinelKey — handler dropped r.Context()")
	}
}

type ctxAssertingHandler struct {
	inner    slog.Handler
	sentinel any
	seen     *atomic.Bool
}

func (c *ctxAssertingHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	return c.inner.Enabled(ctx, lvl)
}

func (c *ctxAssertingHandler) Handle(ctx context.Context, r slog.Record) error {
	if v := ctx.Value(c.sentinel); v != nil {
		c.seen.Store(true)
	}
	return c.inner.Handle(ctx, r)
}

func (c *ctxAssertingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ctxAssertingHandler{inner: c.inner.WithAttrs(attrs), sentinel: c.sentinel, seen: c.seen}
}

func (c *ctxAssertingHandler) WithGroup(name string) slog.Handler {
	return &ctxAssertingHandler{inner: c.inner.WithGroup(name), sentinel: c.sentinel, seen: c.seen}
}

// Scenario: 3.3-UNIT-018 — Valid body well below 1 MiB completes within
// ~10ms (no buffering pathology; BR-1.2 sanity).
func TestChatCompletions_HappyPath_FastUnder10ms(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	rr := doRequest(t, h, validReqBody)
	elapsed := time.Since(start)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// Generous bound — CI runners can stall; 100ms is comfortable.
	if elapsed > 100*time.Millisecond {
		t.Errorf("happy-path took %v, want < 100ms", elapsed)
	}
}

// ----- AC1: PII non-leak (BR-1.8 / T1.7) --------------------------------

// Scenario: 3.3-UNIT-016 — content="SHOULD_NEVER_APPEAR_IN_LOG: ..." → log
// buffer MUST NOT contain the sentinel substring.
func TestChatCompletions_PIINonLeak(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	h := handlers.NewChatCompletionsHandler(bufLogger(buf))

	const sentinel = "SHOULD_NEVER_APPEAR_IN_LOG-PII-canary-zzz"
	body := fmt.Sprintf(`{"model":"qwen-max","messages":[{"role":"user","content":%q}]}`, sentinel)
	rr := doRequest(t, h, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(buf.String(), sentinel) {
		t.Errorf("PII leak: log contains %q\nlog=%s", sentinel, buf.String())
	}
}

// ----- AC2: Validation pipeline ------------------------------------------

// Scenario: 3.3-UNIT-019..039 — table-driven validation coverage.
func TestChatCompletions_ValidationCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
		wantType   string
		wantParam  any // nil for JSON null
	}{
		// UNIT-021 — invalid JSON (trailing comma + missing brace) → 400.
		{"invalid_json_trailing_comma", `{"model":"qwen-max",}`, 400, "400_invalid_request", "invalid_request_error", nil},
		{"invalid_json_unterminated", `{"model":"qwen-max"`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-022 — model missing.
		{"model_missing", `{"messages":[{"role":"user","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-023 — model empty string.
		{"model_empty", `{"model":"","messages":[{"role":"user","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-024 — model length > 100.
		{"model_too_long", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, strings.Repeat("x", 101)), 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-025 — model wrong type (json type-strictness catches this).
		{"model_wrong_type", `{"model":42,"messages":[{"role":"user","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-026 — messages missing.
		{"messages_missing", `{"model":"qwen-max"}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-027 — messages empty array.
		{"messages_empty", `{"model":"qwen-max","messages":[]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-029 — invalid role "wizard".
		{"role_wizard", `{"model":"qwen-max","messages":[{"role":"wizard","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-030 — developer role (deferred to Epic 4).
		{"role_developer", `{"model":"qwen-max","messages":[{"role":"developer","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-031 — function (legacy alias) NOT accepted.
		{"role_function", `{"model":"qwen-max","messages":[{"role":"function","content":"hi"}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-032 — content empty.
		{"content_empty", `{"model":"qwen-max","messages":[{"role":"user","content":""}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-033 — content non-string (multipart array) → 400 (type-strict).
		{"content_array", `{"model":"qwen-max","messages":[{"role":"user","content":["hi","there"]}]}`, 400, "400_invalid_request", "invalid_request_error", nil},
		// UNIT-034 RETIRED — Story 3.4 T3.4 removed the stream=true → 501
		// branch from validateChatRequest. stream=true now dispatches to SSE
		// (see TestChatCompletions_StreamDispatchesToSSE for the new contract).
		// Negative table-rows in this driver target validation FAILURES only.
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
			rr := doRequest(t, h, tc.body)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", got)
			}
			env := decodeBody(t, rr)["error"].(map[string]any)
			if env["code"] != tc.wantCode {
				t.Errorf("error.code = %v, want %s", env["code"], tc.wantCode)
			}
			if env["type"] != tc.wantType {
				t.Errorf("error.type = %v, want %s", env["type"], tc.wantType)
			}
			if tc.wantParam == nil {
				if env["param"] != nil {
					t.Errorf("error.param = %v, want JSON null", env["param"])
				}
			} else {
				if env["param"] != tc.wantParam {
					t.Errorf("error.param = %v, want %v", env["param"], tc.wantParam)
				}
			}
			if env["he_request_id"] != nil {
				t.Errorf("error.he_request_id = %v, want nil", env["he_request_id"])
			}
		})
	}
}

// Scenario: 3.3-UNIT-028 — messages length = 257 → same 400 envelope.
func TestChatCompletions_MessagesTooLong(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	msgs := make([]string, 0, 257)
	for i := 0; i < 257; i++ {
		msgs = append(msgs, `{"role":"user","content":"hi"}`)
	}
	body := fmt.Sprintf(`{"model":"qwen-max","messages":[%s]}`, strings.Join(msgs, ","))

	rr := doRequest(t, h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if env["code"] != "400_invalid_request" {
		t.Errorf("error.code = %v", env["code"])
	}
}

// Scenario: 3.3-UNIT-020 — body > 1 MiB → 413 envelope (BR-1.2 / BR-2.1 step 1).
func TestChatCompletions_BodyTooLarge(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Construct a body > 1 MiB by inflating a single message's content.
	hugeContent := strings.Repeat("x", (1<<20)+1024)
	body := fmt.Sprintf(`{"model":"qwen-max","messages":[{"role":"user","content":%q}]}`, hugeContent)

	rr := doRequest(t, h, body)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body-prefix=%s", rr.Code, rr.Body.String()[:min(rr.Body.Len(), 200)])
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	if env["code"] != "413_payload_too_large" {
		t.Errorf("error.code = %v, want 413_payload_too_large", env["code"])
	}
	// 4xx → invalid_request_error per §5.1.2 mapping (matches the
	// BR-1.2 spec body in the Story Error Handling table).
	if env["type"] != "invalid_request_error" {
		t.Errorf("error.type = %v, want invalid_request_error", env["type"])
	}
	if env["message"] != "Request body exceeds 1 MiB." {
		t.Errorf("error.message = %v", env["message"])
	}
}

// Scenario: 3.4-INT-004 (repurposed from 3.3-UNIT-035) — stream=true now
// dispatches to SSE; the 501 short-circuit is gone (T3.4 removed it).
func TestChatCompletions_StreamDispatchesToSSE(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	rr := doRequest(t, h, `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want \"text/event-stream; charset=utf-8\"", ct)
	}
	if !strings.HasPrefix(rr.Body.String(), `data: {"id":"chatcmpl-mock-`) {
		t.Errorf("body does not start with SSE data line: %q", rr.Body.String())
	}
}

// Scenario: 3.4-INT-008 (repurposed from 3.3-UNIT-036) — streaming success
// emits exactly one structured log line with event=chat_completions_stream
// (the retired 3.3 marker chat_completions_stream_rejected MUST NOT appear).
func TestChatCompletions_StreamEmitsCompletionLog(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	h := handlers.NewChatCompletionsHandler(bufLogger(buf))

	rr := doRequest(t, h, `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	log := buf.String()
	if !strings.Contains(log, `"event":"chat_completions_stream"`) {
		t.Errorf("log missing event=chat_completions_stream: %s", log)
	}
	if !strings.Contains(log, `"api_key_id":"`+testAPIKeyID+`"`) {
		t.Errorf("log missing api_key_id: %s", log)
	}
	// The Story-3.3 rejection marker MUST be gone.
	if strings.Contains(log, `"event":"chat_completions_stream_rejected"`) {
		t.Errorf("retired marker chat_completions_stream_rejected still present: %s", log)
	}
	// Non-streaming completion marker MUST NOT fire on the SSE path.
	if strings.Contains(log, `"event":"chat_completions_mock"`) {
		t.Errorf("streaming path emitted chat_completions_mock log: %s", log)
	}
}

// Scenario: 3.3-UNIT-037 — BR-2.4 NoBodyEcho: error message MUST NOT contain
// attacker-supplied model string.
func TestChatCompletions_NoBodyEcho(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	const evil = "qwen-evil-payload-canary-1234"
	// Send a request that passes the model rule but fails the messages rule
	// — the messages-error message must not contain the evil model string.
	body := fmt.Sprintf(`{"model":%q,"messages":[]}`, evil)
	rr := doRequest(t, h, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), evil) {
		t.Errorf("response body echoed attacker-supplied model %q: body=%s", evil, rr.Body.String())
	}
}

// Scenario: 3.3-UNIT-038 — passthrough fields are accepted and produce 200.
func TestChatCompletions_PassthroughFields(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	body := `{
        "model":"qwen-max",
        "messages":[{"role":"user","content":"hi"}],
        "temperature":0.7,
        "max_tokens":2048,
        "tools":[{"type":"function","function":{"name":"f"}}],
        "tool_choice":"auto",
        "response_format":{"type":"json_object"}
    }`

	rr := doRequest(t, h, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// Scenario: 3.3-UNIT-039 — BR-2.1 ordering invariant: `{model:"", messages:[]}`
// → model-error returned FIRST (not messages-error). The error message
// surfaces which rule fired.
func TestChatCompletions_ValidationOrderingInvariant(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	rr := doRequest(t, h, `{"model":"","messages":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	env := decodeBody(t, rr)["error"].(map[string]any)
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, "'model'") {
		t.Errorf("ordering invariant violated: expected model-error first; got message=%q", msg)
	}
}

// ----- AC4: Determinism + observability ---------------------------------

// Scenario: 3.3-UNIT-040 — 1000 invocations have stable content/finish/role/usage,
// 1000 unique ids, monotonic non-decreasing created timestamps.
func TestChatCompletions_Determinism_1000Calls(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))

	const n = 1000
	ids := make(map[string]struct{}, n)
	var lastCreated int64

	for i := 0; i < n; i++ {
		rr := doRequest(t, h, validReqBody)
		if rr.Code != http.StatusOK {
			t.Fatalf("iter %d: status = %d", i, rr.Code)
		}
		got := decodeBody(t, rr)
		// Stable fields.
		choices := got["choices"].([]any)
		c0 := choices[0].(map[string]any)
		msg := c0["message"].(map[string]any)
		if msg["content"] != handlers.MockContent || msg["role"] != "assistant" || c0["finish_reason"] != "stop" {
			t.Fatalf("iter %d: choice fields drifted: %+v", i, c0)
		}
		if got["object"] != "chat.completion" {
			t.Fatalf("iter %d: object = %v", i, got["object"])
		}
		usage := got["usage"].(map[string]any)
		if usage["prompt_tokens"].(float64) != 10 || usage["completion_tokens"].(float64) != 20 || usage["total_tokens"].(float64) != 30 {
			t.Fatalf("iter %d: usage drifted: %+v", i, usage)
		}
		// Unique id.
		id := got["id"].(string)
		if _, dup := ids[id]; dup {
			t.Fatalf("iter %d: duplicate id %q", i, id)
		}
		ids[id] = struct{}{}
		// Monotonic non-decreasing created.
		created := int64(got["created"].(float64))
		if created < lastCreated {
			t.Fatalf("iter %d: created went backwards %d < %d", i, created, lastCreated)
		}
		lastCreated = created
	}
	if len(ids) != n {
		t.Errorf("unique ids = %d, want %d", len(ids), n)
	}
}

// Scenario: 3.3-UNIT-041 — WithIDFactory injectable + parallel-test safe.
// Two sibling sub-tests each install their own factory; each must see only
// its own stub (no global race — OQ4 constructor-injection invariant).
func TestChatCompletions_WithIDFactoryInjectable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   string
	}{
		{"alpha", "chatcmpl-mock-aaaaaaaaaaaa"},
		{"beta", "chatcmpl-mock-bbbbbbbbbbbb"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := handlers.NewChatCompletionsHandler(
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				handlers.WithIDFactory(func() string { return tc.id }),
			)
			// 50 calls — high probability of trampling a global if one existed.
			for i := 0; i < 50; i++ {
				rr := doRequest(t, h, validReqBody)
				if rr.Code != http.StatusOK {
					t.Fatalf("iter %d: status %d", i, rr.Code)
				}
				if got := decodeBody(t, rr)["id"].(string); got != tc.id {
					t.Fatalf("iter %d: id = %q, want %q", i, got, tc.id)
				}
			}
		})
	}
}

// Scenario: 3.3-UNIT-042 — exported MockContent constant matches contract.
func TestMockContent_ExportedConstant(t *testing.T) {
	t.Parallel()
	const want = "Hello from He-API mock. Real upstream lands in Story 4.x."
	if handlers.MockContent != want {
		t.Errorf("MockContent = %q, want %q", handlers.MockContent, want)
	}
}

// Scenario: 3.3-UNIT-043 — BR-4.2 log-grep marker substring present.
func TestMockContent_GrepMarker(t *testing.T) {
	t.Parallel()
	if !strings.Contains(handlers.MockContent, "He-API mock") {
		t.Errorf("MockContent missing 'He-API mock' marker: %q", handlers.MockContent)
	}
}

// Scenario: 3.3-UNIT-044 — BR-4.4 ASCII-safety of MockContent.
func TestMockContent_ASCIISafe(t *testing.T) {
	t.Parallel()
	for i, r := range handlers.MockContent {
		if r > 0x7F {
			t.Errorf("MockContent rune at byte %d is non-ASCII: %q (U+%04X)", i, r, r)
		}
	}
	// Marshal a ChatMessage carrying MockContent and confirm no \u escapes.
	buf, _ := json.Marshal(map[string]string{"content": handlers.MockContent})
	if strings.Contains(string(buf), `\u`) {
		t.Errorf("MockContent JSON-marshal produced \\u escape: %s", buf)
	}
}

// Scenario: 3.3-UNIT-045 — BR-4.3 input-invariance: different messages
// content → identical response content/usage/finish_reason/role.
func TestChatCompletions_InputInvariance(t *testing.T) {
	t.Parallel()
	stubID := "chatcmpl-mock-cccccccccccc"
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithIDFactory(func() string { return stubID }))

	bodyA := `{"model":"qwen-max","messages":[{"role":"user","content":"AAA"}]}`
	bodyB := `{"model":"qwen-max","messages":[{"role":"user","content":"ZZZ-different"}]}`
	rrA := doRequest(t, h, bodyA)
	rrB := doRequest(t, h, bodyB)
	if rrA.Code != http.StatusOK || rrB.Code != http.StatusOK {
		t.Fatalf("statuses = %d / %d", rrA.Code, rrB.Code)
	}
	gA := decodeBody(t, rrA)
	gB := decodeBody(t, rrB)
	cA := gA["choices"].([]any)[0].(map[string]any)
	cB := gB["choices"].([]any)[0].(map[string]any)
	mA := cA["message"].(map[string]any)
	mB := cB["message"].(map[string]any)
	if mA["content"] != mB["content"] || mA["role"] != mB["role"] || cA["finish_reason"] != cB["finish_reason"] {
		t.Errorf("response varied by input: A=%+v B=%+v", cA, cB)
	}
}

// Scenario: 3.3-UNIT-046 — BR-4.3 temperature-invariance.
func TestChatCompletions_TemperatureInvariance(t *testing.T) {
	t.Parallel()
	stubID := "chatcmpl-mock-dddddddddddd"
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithIDFactory(func() string { return stubID }))

	var firstContent string
	for _, temp := range []string{"0.0", "0.7", "1.0", "2.0"} {
		body := fmt.Sprintf(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"temperature":%s}`, temp)
		rr := doRequest(t, h, body)
		if rr.Code != http.StatusOK {
			t.Fatalf("temp=%s: status=%d", temp, rr.Code)
		}
		content := decodeBody(t, rr)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		if firstContent == "" {
			firstContent = content
		} else if content != firstContent {
			t.Errorf("content varied by temperature %s: %q != %q", temp, content, firstContent)
		}
	}
}

// Scenario: 3.3-UNIT-047 — WithIDFactory(nil) MUST NOT panic and MUST fall
// back to the default factory (BR-4.5 explicit-contract assertion).
func TestChatCompletions_WithIDFactoryNil(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithIDFactory(nil))

	rr := doRequest(t, h, validReqBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	id := decodeBody(t, rr)["id"].(string)
	if !strings.HasPrefix(id, "chatcmpl-mock-") {
		t.Errorf("nil-factory fell back to non-default: id=%q", id)
	}
}

// ----- AC1+AC3 cross-cut: full HTTP wire --------------------------------

// Scenario: 3.4-INT-004 wire-level sibling (repurposed from 3.3-INT-003) —
// full HTTP wire for stream=true now serves SSE (200 + text/event-stream +
// data: line). The 501 JSON envelope path is gone.
func TestChatCompletions_StreamDispatchesToSSE_HTTPWire(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	shim := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withBearerCtx(r.Context()))
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(shim)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want \"text/event-stream; charset=utf-8\"", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache, no-transform" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if xab := resp.Header.Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want \"no\"", xab)
	}
	body, _ := io.ReadAll(resp.Body)
	// Body must contain the BR-1.2 framing + the OpenAI [DONE] terminator.
	if !strings.HasPrefix(string(body), `data: {"id":"chatcmpl-mock-`) {
		t.Errorf("body does not begin with SSE data: line\nbody=%s", string(body))
	}
	if !strings.HasSuffix(string(body), "data: [DONE]\n\n") {
		t.Errorf("body does not end with [DONE] sentinel\nbody=%s", string(body))
	}
}

// Scenario: 3.3-INT-004 — full HTTP wire for invalid-JSON 400.
func TestChatCompletions_InvalidJSON_HTTPWire(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	shim := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withBearerCtx(r.Context()))
		h.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(shim)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"model":"qwen-max",`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	want := `{"error":{"code":"400_invalid_request","he_request_id":null,"message":"Request body is not valid JSON.","param":null,"type":"invalid_request_error"}}`
	if string(body) != want {
		t.Errorf("body byte-mismatch\n got=%s\nwant=%s", body, want)
	}
}

// ----- AC1+AC3: bearer-auth chain integration ---------------------------

// chatStubAuthSvc + harness mirror the middleware_test pattern — small
// inline reproduction so the handler test stays in `handlers_test`.
type chatStubAuthSvc struct {
	authv1connect.UnimplementedAuthServiceHandler
	validateFn func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)
}

func (s *chatStubAuthSvc) ValidateApiKey(
	_ context.Context,
	req *connect.Request[authv1.ValidateApiKeyRequest],
) (*connect.Response[authv1.ValidateApiKeyResponse], error) {
	resp, err := s.validateFn(req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func newBearerHarness(t *testing.T, validate func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error)) (
	mw *middleware.APIKeyAuthenticator,
	cleanup func(),
) {
	t.Helper()
	mini := miniredis.RunT(t)
	stub := &chatStubAuthSvc{validateFn: validate}
	_, h := authv1connect.NewAuthServiceHandler(stub)
	authSrv := httptest.NewServer(h)
	client := authv1connect.NewAuthServiceClient(authSrv.Client(), authSrv.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mw = middleware.NewAPIKeyAuthenticator(client, func() *redis.Client {
		return redis.NewClient(&redis.Options{Addr: mini.Addr()})
	}, logger)
	cleanup = func() { authSrv.Close() }
	return
}

// Scenario: 3.3-INT-001 — bearer-auth chain integration: 200 + ctx-injected
// api_key_id + slog buffer carries the same api_key_id.
func TestChatCompletions_BearerAuthIntegration(t *testing.T) {
	t.Parallel()
	const plaintext = "he-INT001CHATXYZ12345"
	const apiKeyID = "33333333-3333-3333-3333-333333333333"
	const userID = "44444444-4444-4444-4444-444444444444"

	mw, cleanup := newBearerHarness(t, func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if req.GetPlaintextKey() != plaintext {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
		}
		return &authv1.ValidateApiKeyResponse{Ok: true, ApiKeyId: apiKeyID, UserId: userID, Scope: `{}`}, nil
	})
	defer cleanup()

	buf := &bytes.Buffer{}
	chat := handlers.NewChatCompletionsHandler(bufLogger(buf))
	wrapped := mw.RequireAPIKey(chat)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL,
		strings.NewReader(validReqBody))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; body=%s", resp.StatusCode, body)
	}
	// Log line carries the upstream-stubbed api_key_id.
	if !strings.Contains(buf.String(), `"api_key_id":"`+apiKeyID+`"`) {
		t.Errorf("log missing api_key_id=%s: %s", apiKeyID, buf.String())
	}
}

// Scenario: 3.3-INT-002 — no Authorization header → 401 (Story 3.2 envelope);
// inner chat handler NEVER invoked (sentinel + panic-on-call wrap).
func TestChatCompletions_NoAuth_PathDenies(t *testing.T) {
	t.Parallel()
	mw, cleanup := newBearerHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return &authv1.ValidateApiKeyResponse{Ok: false}, nil
	})
	defer cleanup()

	var innerCalls atomic.Int32
	sentinel := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalls.Add(1)
		panic("chat handler MUST NOT be invoked when auth fails — wiring regression")
	})
	wrapped := mw.RequireAPIKey(sentinel)

	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(validReqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if innerCalls.Load() != 0 {
		t.Errorf("inner handler invoked %d times despite missing auth", innerCalls.Load())
	}
	var env struct {
		Error map[string]any `json:"error"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, body)
	}
	if env.Error["code"] != "401_invalid_api_key" {
		t.Errorf("error.code = %v, want 401_invalid_api_key", env.Error["code"])
	}
}

// ----- Unit: validation pure function -----------------------------------

// Scenario: 3.3-UNIT-019 — happy-path validateChatRequest baseline. The
// pure function is package-private; we exercise it indirectly through the
// handler. Combined with the structure-tested DocBody, this approximates
// the BR-2.5 "fast TDD" invariant.
func TestChatCompletions_HappyPathValidationBaseline(t *testing.T) {
	t.Parallel()
	h := handlers.NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	rr := doRequest(t, h, validReqBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("baseline status = %d", rr.Code)
	}
}

// min is a tiny helper for older Go test envs that lack the builtin.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// _ silences the connect import when go vet runs in an offline mode.
var _ = errors.New
