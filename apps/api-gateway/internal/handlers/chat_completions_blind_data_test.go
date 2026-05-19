// Story 4.1 QA Round 1 fixes — Cross-chain invariant tests closing
// BLIND-DATA-001 (request-id three-way equality) + BLIND-DATA-002
// (token-usage byte equality) P0 gaps surfaced by QA Turing.
//
// Approach: the gateway-side path under test is the REAL handlers.NewChat-
// CompletionsHandler with serveAdapterNonStream / serveAdapterStream. The
// adapter-side observation is the inbound X-He-Request-Id header captured
// by `slogEmittingHandle`, which faithfully reproduces the real adapter's
// BR-1.5 + BR-3.6 slog emission (see
// apps/adapters/deepseek/internal/adapter.go:logRequestEnd /
// logUpstreamError). The adapter's own contract is tested independently
// in apps/adapters/deepseek/internal/adapter_test.go (UNIT-040) +
// apps/adapters/deepseek/tests/adapter_test.go (INT-005 + INT-011); this
// file asserts the gateway↔adapter cross-chain invariant in a single
// atomic test as required by the test-design's "Gate Criteria for Dev →
// Review".
package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	gwrequestid "github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// slogEmittingHandle is a fakeHandle variant that emits a slog record
// mirroring the real DeepSeek adapter's BR-3.6 (`adapter_chat_request_end`)
// or BR-1.4 (`adapter_chat_upstream_error`) emission. The inbound
// `X-He-Request-Id` header drives the `he_request_id` slog attribute, so
// the test can assert gateway-stamped header == adapter slog attribute on
// the same wire as the production code path.
//
// Token-usage attributes match the configured chunk's Usage so the
// BLIND-DATA-002 byte-equality test holds the chain accountable.
type slogEmittingHandle struct {
	mu         sync.Mutex
	logger     *slog.Logger
	chunks     []*adapterv1.ChatChunk
	terminalUsage *adapterv1.Usage
	upstreamErr error // optional — when non-nil, emits adapter_chat_upstream_error then returns the error
}

func (h *slogEmittingHandle) Chat(ctx context.Context, req *adapterv1.ChatRequest, headers http.Header) (adapterclient.Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	heRequestID := headers.Get("X-He-Request-Id")
	if heRequestID == "" {
		// Fall back to proto field (defence-in-depth parity with
		// adapter.Chat where the inbound header is silently lifted onto
		// req.HeRequestId when missing on the proto side).
		heRequestID = req.HeRequestId
	}

	if h.upstreamErr != nil {
		// Mirror apps/adapters/deepseek/internal/adapter.go:logUpstreamError.
		h.logger.LogAttrs(ctx, slog.LevelError, "adapter_chat_upstream_error",
			slog.String("event", "adapter_chat_upstream_error"),
			slog.String("he_request_id", heRequestID),
			slog.String("model", req.Model),
			slog.Int("messages_count", len(req.Messages)),
			slog.Int("upstream_status_code", 503),
			slog.String("upstream_error_kind", "upstream_5xx"),
		)
		return nil, h.upstreamErr
	}

	// Mirror apps/adapters/deepseek/internal/adapter.go:logRequestEnd.
	pt, ct, tt := int32(0), int32(0), int32(0)
	if h.terminalUsage != nil {
		pt, ct, tt = h.terminalUsage.PromptTokens, h.terminalUsage.CompletionTokens, h.terminalUsage.TotalTokens
	}
	h.logger.LogAttrs(ctx, slog.LevelInfo, "adapter_chat_request_end",
		slog.String("event", "adapter_chat_request_end"),
		slog.String("he_request_id", heRequestID),
		slog.String("model", req.Model),
		slog.Int("messages_count", len(req.Messages)),
		slog.Int("prompt_tokens", int(pt)),
		slog.Int("completion_tokens", int(ct)),
		slog.Int("total_tokens", int(tt)),
		slog.Int("upstream_status_code", 200),
	)
	return &fakeStream{chunks: h.chunks}, nil
}

// findRecord returns the first JSON-encoded slog record in buf whose
// `event` attribute matches name, or fails the test.
func findRecord(t *testing.T, buf *bytes.Buffer, name string) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec["event"] == name {
			return rec
		}
	}
	t.Fatalf("no slog record with event=%q in adapter logs:\n%s", name, buf.String())
	return nil
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// doRequestWithMiddleware wraps the handler in the real gwrequestid.RequestID
// middleware so the test observes the production header-stamping path.
// withBearerCtx injects the API-key context as the production bearer-auth
// middleware would.
func doRequestWithMiddleware(t *testing.T, h *handlers.ChatCompletionsHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	wrapped := gwrequestid.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withBearerCtx(r.Context()))
		h.ServeHTTP(w, r)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)
	return rr
}

// ----- BLIND-DATA-001 — request-id three-way equality ---------------------

// 4.1-BLIND-DATA-001 (P0) — Success path: response header X-He-Request-Id
// must equal the adapter's BR-3.6 slog `he_request_id` attribute. Success
// bodies carry no `error.he_request_id` field, so the body-side leg of
// the three-way invariant collapses to header == slog. The error-path
// sibling test below covers the full three-way.
func TestBlindData001_RequestId_ThreeWayEquality_NonStreaming_Success(t *testing.T) {
	buf := &bytes.Buffer{}
	adapterLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chunk := canonicalAdapterChunk()
	handle := &slogEmittingHandle{
		logger:        adapterLogger,
		chunks:        []*adapterv1.ChatChunk{chunk},
		terminalUsage: chunk.Usage,
	}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": handle,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequestWithMiddleware(t, h,
		`{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	headerID := rr.Header().Get("X-He-Request-Id")
	if headerID == "" {
		t.Fatalf("response header X-He-Request-Id is empty")
	}
	rec := findRecord(t, buf, "adapter_chat_request_end")
	slogID, _ := rec["he_request_id"].(string)
	if slogID != headerID {
		t.Fatalf("BLIND-DATA-001 success-path mismatch:\n  header X-He-Request-Id = %q\n  adapter slog he_request_id = %q",
			headerID, slogID)
	}
}

// 4.1-BLIND-DATA-001 (P0) — Error path: full three-way equality across
// response header, response body `error.he_request_id`, and adapter slog
// `he_request_id`. Upstream surfaces Connect-RPC `Code.Unavailable`; the
// gateway emits the BR-1.4 502 envelope via openaierr.Write, which stamps
// the body-side he_request_id from the same ctx slot the header
// middleware uses.
func TestBlindData001_RequestId_ThreeWayEquality_NonStreaming_ErrorPath(t *testing.T) {
	buf := &bytes.Buffer{}
	adapterLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handle := &slogEmittingHandle{
		logger:      adapterLogger,
		upstreamErr: connect.NewError(connect.CodeUnavailable, errors.New("upstream 503")),
	}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": handle,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequestWithMiddleware(t, h,
		`{"model":"deepseek-v3","messages":[{"role":"user","content":"x"}]}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rr.Code, rr.Body.String())
	}
	headerID := rr.Header().Get("X-He-Request-Id")
	if headerID == "" {
		t.Fatalf("response header X-He-Request-Id is empty")
	}
	var envelope struct {
		Error struct {
			Code        string `json:"code"`
			HeRequestID string `json:"he_request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rr.Body.String())
	}
	if envelope.Error.Code != "502_upstream_unavailable" {
		t.Fatalf("error.code = %q, want 502_upstream_unavailable", envelope.Error.Code)
	}
	bodyID := envelope.Error.HeRequestID
	rec := findRecord(t, buf, "adapter_chat_upstream_error")
	slogID, _ := rec["he_request_id"].(string)
	if !(headerID == bodyID && bodyID == slogID) {
		t.Fatalf("BLIND-DATA-001 three-way invariant violated:\n  header = %q\n  body   = %q\n  slog   = %q",
			headerID, bodyID, slogID)
	}
}

// 4.1-BLIND-DATA-001 — Streaming success-path companion. SSE bodies carry
// no JSON envelope, so the body-side leg collapses to header == slog
// (parity with the non-streaming success test). The error-path streaming
// case is covered by the post-flush SSE error frame, which threads
// he_request_id through writeSSEErrorFrame (already exercised by
// TestServeStream_PostFlushFailure_EmitsSSEErrorFrameThenDONE).
func TestBlindData001_RequestId_ThreeWayEquality_Streaming_Success(t *testing.T) {
	buf := &bytes.Buffer{}
	adapterLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chunks := canonicalStreamingChunks()
	terminalUsage := chunks[len(chunks)-1].Usage
	handle := &slogEmittingHandle{
		logger:        adapterLogger,
		chunks:        chunks,
		terminalUsage: terminalUsage,
	}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": handle,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequestWithMiddleware(t, h,
		`{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	headerID := rr.Header().Get("X-He-Request-Id")
	if headerID == "" {
		t.Fatalf("response header X-He-Request-Id is empty")
	}
	rec := findRecord(t, buf, "adapter_chat_request_end")
	slogID, _ := rec["he_request_id"].(string)
	if slogID != headerID {
		t.Fatalf("BLIND-DATA-001 streaming success-path mismatch:\n  header=%q\n  slog  =%q", headerID, slogID)
	}
}

// ----- BLIND-DATA-002 — token-usage byte equality -------------------------

// 4.1-BLIND-DATA-002 (P0) — Non-streaming token-usage byte equality:
// adapter slog `total_tokens` == gateway response body
// `usage.total_tokens`. The SDK-observed leg of the three-way is covered
// by CONTRACT-003; this test asserts the gateway↔adapter segment that
// CONTRACT-003 inherits. Token counts in canonicalAdapterChunk
// (PT=5/CT=8/TT=13) are intentionally distinct so a zero-default drift
// would surface.
func TestBlindData002_TokenUsage_ByteEquality_NonStreaming(t *testing.T) {
	buf := &bytes.Buffer{}
	adapterLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chunk := canonicalAdapterChunk()
	handle := &slogEmittingHandle{
		logger:        adapterLogger,
		chunks:        []*adapterv1.ChatChunk{chunk},
		terminalUsage: chunk.Usage,
	}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": handle,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequestWithMiddleware(t, h,
		`{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Usage.PromptTokens != 5 || body.Usage.CompletionTokens != 8 || body.Usage.TotalTokens != 13 {
		t.Fatalf("gateway body usage = %+v, want (PT=5 CT=8 TT=13)", body.Usage)
	}
	rec := findRecord(t, buf, "adapter_chat_request_end")
	if got := jsonInt(rec["total_tokens"]); got != body.Usage.TotalTokens {
		t.Fatalf("BLIND-DATA-002 byte-equality violated: adapter slog total_tokens=%d, gateway body total_tokens=%d",
			got, body.Usage.TotalTokens)
	}
	if got := jsonInt(rec["prompt_tokens"]); got != body.Usage.PromptTokens {
		t.Fatalf("BLIND-DATA-002: slog prompt_tokens=%d != body prompt_tokens=%d", got, body.Usage.PromptTokens)
	}
	if got := jsonInt(rec["completion_tokens"]); got != body.Usage.CompletionTokens {
		t.Fatalf("BLIND-DATA-002: slog completion_tokens=%d != body completion_tokens=%d", got, body.Usage.CompletionTokens)
	}
}

// 4.1-BLIND-DATA-002 (P0) — Streaming token-usage byte equality: adapter
// slog `total_tokens` == the SSE tail-usage frame's `usage.total_tokens`
// (which IS the byte stream the SDK reads). canonicalStreamingChunks
// emits PT=4/CT=2/TT=6 on the terminal chunk.
func TestBlindData002_TokenUsage_ByteEquality_Streaming(t *testing.T) {
	buf := &bytes.Buffer{}
	adapterLogger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chunks := canonicalStreamingChunks()
	terminalUsage := chunks[len(chunks)-1].Usage
	handle := &slogEmittingHandle{
		logger:        adapterLogger,
		chunks:        chunks,
		terminalUsage: terminalUsage,
	}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"deepseek-v3": handle,
	})
	h := handlers.NewChatCompletionsHandler(nil, handlers.WithAdapterRegistry(reg))

	rr := doRequestWithMiddleware(t, h,
		`{"model":"deepseek-v3","messages":[{"role":"user","content":"Hi"}],"stream":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	bodyStr := rr.Body.String()
	want := `"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}`
	if !strings.Contains(bodyStr, want) {
		t.Fatalf("SSE body missing expected tail-usage frame %s; body:\n%s", want, bodyStr)
	}
	rec := findRecord(t, buf, "adapter_chat_request_end")
	if got := jsonInt(rec["total_tokens"]); got != 6 {
		t.Fatalf("BLIND-DATA-002 streaming: adapter slog total_tokens=%d, want 6 (matches SSE body)", got)
	}
}
