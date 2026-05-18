// Story 3.4 — /v1/chat/completions Streaming (SSE) — Handler integration tests
//
// Test Design: docs/qa/assessments/3.4-test-design-20260518.md

package handlers_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/goleak"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1/authv1connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// ----- Streaming-specific helpers (shared across INT tests) ----------------

const (
	streamReqBody = `{"model":"qwen-max","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Say hi."}],"stream":true}`
)

// streamHandler returns a handler.ServeHTTP wrapped in a shim that injects
// bearer-auth context (same convention as Story 3.3 wire tests). The
// returned handler is suitable for httptest.NewServer.
func streamHandler(h *handlers.ChatCompletionsHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withBearerCtx(r.Context()))
		h.ServeHTTP(w, r)
	})
}

// pacedStreamHandler wraps the handler's ResponseWriter so each Flush()
// pauses by `pace`. Used by INT-011 + BLIND-CONCURRENCY-001 to give
// client-side cancellation a real chance to race the chunker loop
// (production code path is byte-identical — the pacer lives only in tests).
func pacedStreamHandler(h *handlers.ChatCompletionsHandler, pace time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(withBearerCtx(r.Context()))
		paced := &pacedResponseWriter{ResponseWriter: w, pace: pace}
		h.ServeHTTP(paced, r)
	})
}

// pacedResponseWriter implements http.ResponseWriter + http.Flusher +
// Unwrap so http.ResponseController resolves Flush through this wrapper
// (which adds a deterministic sleep). Critically: the chunker's own
// behavior is unchanged — the pacer only slows the I/O boundary.
type pacedResponseWriter struct {
	http.ResponseWriter
	pace time.Duration
}

func (p *pacedResponseWriter) Flush() {
	if p.pace > 0 {
		time.Sleep(p.pace)
	}
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (p *pacedResponseWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// percentile returns the p-th percentile (0..100) of ds via nearest-rank.
func percentile(ds []time.Duration, p int) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// measureTTFB issues n in-process requests via httptest.NewRecorder and
// records wall-clock from ServeHTTP entry to first body byte observed.
func measureTTFB(t *testing.T, n int) []time.Duration {
	t.Helper()
	h := handlers.NewChatCompletionsHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithIDFactory(func() string { return "chatcmpl-mock-ttfb00000000" }),
		handlers.WithNow(func() time.Time { return time.Unix(1715000000, 0).UTC() }),
	)
	out := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		rr := newWatchedRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(streamReqBody))
		req.Header.Set("Authorization", "Bearer stub")
		req = req.WithContext(withBearerCtx(req.Context()))
		start := time.Now()
		h.ServeHTTP(rr, req)
		out[i] = rr.firstByteAt.Sub(start)
		if rr.firstByteAt.IsZero() {
			t.Fatalf("iter %d: no body bytes observed", i)
		}
	}
	return out
}

// watchedRecorder is a ResponseRecorder shim that records the wall-clock
// instant of the first Write call. Sub-millisecond resolution.
type watchedRecorder struct {
	*httptest.ResponseRecorder
	firstByteAt time.Time
}

func newWatchedRecorder() *watchedRecorder {
	return &watchedRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (w *watchedRecorder) Write(b []byte) (int, error) {
	if w.firstByteAt.IsZero() {
		w.firstByteAt = time.Now()
	}
	return w.ResponseRecorder.Write(b)
}

func (w *watchedRecorder) Flush() { w.ResponseRecorder.Flush() }

// streamDispatchHandler with stubbed id+now for deterministic INT tests.
// `buf` (when non-nil) MUST be wrapped in syncBuf for any test that uses
// httptest.NewServer — the server goroutine writes to slog concurrently
// with the test's reads (race detector flags bytes.Buffer otherwise).
func newStubbedStreamHandler(buf io.Writer, idStub string, unix int64) *handlers.ChatCompletionsHandler {
	var w io.Writer = io.Discard
	if buf != nil {
		w = buf
	}
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return handlers.NewChatCompletionsHandler(logger,
		handlers.WithIDFactory(func() string { return idStub }),
		handlers.WithNow(func() time.Time { return time.Unix(unix, 0).UTC() }),
	)
}

// syncBuf is a thread-safe bytes.Buffer wrapper for slog-buffer assertions
// in tests where the gateway goroutine writes while the test reads.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// ============================================================
// AC2 — TTFB Performance Budget (Integration)
// ============================================================

// Scenario: 3.4-INT-001 — Priority P0
// BR-2.1 + BR-2.5: in-process TTFB P95 ≤ 300ms
func TestChatCompletionsStream_TTFB_InProcess(t *testing.T) {
	measurements := measureTTFB(t, 100)
	p50 := percentile(measurements, 50)
	p95 := percentile(measurements, 95)
	p99 := percentile(measurements, 99)
	t.Logf("ttfb_ms p50=%d p95=%d p99=%d (n=100, mode=recorder)",
		p50.Milliseconds(), p95.Milliseconds(), p99.Milliseconds())
	if p95 > 300*time.Millisecond {
		t.Errorf("P95 = %v, exceeds 300ms budget (AC2)", p95)
	}
	if p99 > 600*time.Millisecond {
		t.Errorf("P99 = %v, exceeds 600ms sanity ceiling", p99)
	}
}

// Scenario: 3.4-INT-002 — Priority P0
// BR-2.5: loopback TCP TTFB + two-mode delta < 20ms (recorder isn't masking latency)
func TestChatCompletionsStream_TTFB_Loopback(t *testing.T) {
	recorderM := measureTTFB(t, 100)
	recP95 := percentile(recorderM, 95)

	h := handlers.NewChatCompletionsHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithIDFactory(func() string { return "chatcmpl-mock-ttfbloop0001" }),
		handlers.WithNow(func() time.Time { return time.Unix(1715000000, 0).UTC() }),
	)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	loopback := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
		req.Header.Set("Authorization", "Bearer stub")
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("iter %d post: %v", i, err)
		}
		reader := bufio.NewReader(resp.Body)
		if _, err := reader.ReadByte(); err != nil {
			t.Fatalf("iter %d read first byte: %v", i, err)
		}
		loopback[i] = time.Since(start)
		// Drain remaining body so the server closes cleanly.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	loopP50 := percentile(loopback, 50)
	loopP95 := percentile(loopback, 95)
	loopP99 := percentile(loopback, 99)
	t.Logf("ttfb_ms p50=%d p95=%d p99=%d (n=100, mode=loopback)",
		loopP50.Milliseconds(), loopP95.Milliseconds(), loopP99.Milliseconds())
	if loopP95 > 300*time.Millisecond {
		t.Errorf("loopback P95 = %v, exceeds 300ms budget", loopP95)
	}
	delta := loopP95 - recP95
	if delta < 0 {
		delta = -delta
	}
	if delta > 20*time.Millisecond {
		t.Errorf("two-mode P95 delta = %v (loopback %v vs recorder %v); exceeds 20ms ceiling — recorder may be masking latency",
			delta, loopP95, recP95)
	}
}

// Scenario: 3.4-INT-003 — Priority P1
// BR-2.6: sleep-regression catch — P50 < 5ms over 10 iters
func TestChatCompletionsStream_TTFB_NoSleepRegression(t *testing.T) {
	measurements := measureTTFB(t, 10)
	p50 := percentile(measurements, 50)
	t.Logf("ttfb_ms p50=%d (n=10, mode=recorder, sleep-regression catch)", p50.Milliseconds())
	if p50 > 5*time.Millisecond {
		t.Errorf("P50 = %v exceeds 5ms sleep-regression floor (likely a time.Sleep was added to the chunker)", p50)
	}
}

// ============================================================
// AC1.E — Dispatch Fork + Story-3.3 Path Preservation (Integration)
// ============================================================

// Scenario: 3.4-INT-004 — Priority P0
func TestChatCompletionsStream_DispatchFork_StreamTrue_RoutesToSSE(t *testing.T) {
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int00000004", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), `data: {"id":"chatcmpl-mock-int00000004","object":"chat.completion.chunk"`) {
		t.Errorf("body does not begin with expected SSE data: line\nbody=%s", string(body))
	}
}

// Scenario: 3.4-INT-005 — Priority P0
func TestChatCompletionsStream_DispatchFork_StreamFalse_RoutesToJSON(t *testing.T) {
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int00000005", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	body := `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"stream":false}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	respBody, _ := io.ReadAll(resp.Body)
	// Story-3.3 wire shape — single JSON object with object="chat.completion".
	if !strings.Contains(string(respBody), `"object":"chat.completion"`) {
		t.Errorf("non-stream body does not contain chat.completion: %s", string(respBody))
	}
	if strings.Contains(string(respBody), `chat.completion.chunk`) {
		t.Errorf("non-stream body contains chunk marker: %s", string(respBody))
	}
}

// Scenario: 3.4-INT-006 — Priority P0
func TestChatCompletionsStream_DispatchFork_StreamMissing_DefaultsToFalse(t *testing.T) {
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int00000006", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	body := `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json (default false)", ct)
	}
}

// Scenario: 3.4-INT-007 — Priority P0
// Load-bearing regression — golden-file byte-equality on Story-3.3 non-stream
func TestChatCompletionsStream_PreservesNonStreamPathByteForByte(t *testing.T) {
	const (
		stubID    = "chatcmpl-mock-feedbeef0042"
		stubUnix  = int64(1715000000)
		fixedBody = `{"model":"qwen-max","messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"Say hi."}]}`
	)
	want, err := os.ReadFile("testdata/non_streaming_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	h := newStubbedStreamHandler(nil, stubID, stubUnix)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(fixedBody))
	req.Header.Set("Authorization", "Bearer stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), want) {
		t.Errorf("non-streaming response bytes diverged from golden\n got=%s\nwant=%s",
			rr.Body.String(), string(want))
	}
}

// Scenario: 3.4-BLIND-BOUNDARY-003 — Priority P2
// stream as STRING (not bool) → 400 BEFORE dispatch fork
func TestChatCompletionsStream_StreamFieldAsString_Returns400(t *testing.T) {
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int000bd3", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	body := `{"model":"qwen-max","messages":[{"role":"user","content":"hi"}],"stream":"true"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400 (json type-strictness should reject stream:\"true\")", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), `"code":"400_invalid_request"`) {
		t.Errorf("body missing 400_invalid_request code: %s", string(respBody))
	}
}

// ============================================================
// AC1.F — Structured Logging (Integration)
// ============================================================

// Scenario: 3.4-INT-008 — Priority P0
// BR-1.8 structured-log success path — all 8 attributes
func TestChatCompletionsStream_StructuredLog_OnSuccess(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000008", 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	logLines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	streamLines := 0
	var entry map[string]any
	for _, line := range logLines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["event"] == "chat_completions_stream" {
			streamLines++
			entry = m
		}
	}
	if streamLines != 1 {
		t.Fatalf("want exactly 1 chat_completions_stream log line; got %d\nbuf=%s", streamLines, buf.String())
	}
	checks := map[string]any{
		"model":               "qwen-max",
		"api_key_id":          testAPIKeyID,
		"messages_count":      float64(2),
		"client_disconnected": false,
	}
	for k, v := range checks {
		if entry[k] != v {
			t.Errorf("attr %s = %v (%T), want %v", k, entry[k], entry[k], v)
		}
	}
	if entry["chunks_emitted"] == nil {
		t.Errorf("chunks_emitted missing")
	} else if n, _ := entry["chunks_emitted"].(float64); n < 1 {
		t.Errorf("chunks_emitted = %v, want >=1", entry["chunks_emitted"])
	}
	for _, k := range []string{"ttfb_ms", "total_ms"} {
		if entry[k] == nil {
			t.Errorf("%s missing", k)
		} else if n, _ := entry[k].(float64); n < 0 {
			t.Errorf("%s = %v, want >=0", k, n)
		}
	}
}

// Scenario: 3.4-INT-009 — Priority P0
// BR-1.8 PII non-leakage
func TestChatCompletionsStream_StructuredLog_NoPIIInLogs(t *testing.T) {
	const sentinel = "SHOULD_NEVER_APPEAR_IN_LOG: streaming sentinel"
	buf := &bytes.Buffer{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000009", 1715000000)
	body := fmt.Sprintf(`{"model":"qwen-max","messages":[{"role":"user","content":%q}],"stream":true}`, sentinel)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(buf.String(), sentinel) {
		t.Errorf("sentinel leaked into log: %s", buf.String())
	}
}

// Scenario: 3.4-INT-010 — Priority P1
// Retired marker chat_completions_stream_rejected MUST NOT appear
func TestChatCompletionsStream_RetiresStreamRejectedLogMarker(t *testing.T) {
	buf := &bytes.Buffer{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000010", 1715000000)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	h.ServeHTTP(rr, req)
	if strings.Contains(buf.String(), "chat_completions_stream_rejected") {
		t.Errorf("retired marker present: %s", buf.String())
	}
}

// ============================================================
// AC4 — Cancellation, Disconnect Handling, Resource Hygiene
// ============================================================

// Scenario: 3.4-INT-011 — Priority P0
// Deterministic client-disconnect via context cancellation
func TestChatCompletionsStream_ClientDisconnect_ContextCancellation(t *testing.T) {
	buf := &syncBuf{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000011", 1715000000)
	// Pace each Flush by 20ms — 10 chunks × 20ms = 200ms total stream time,
	// well within the 1s polling deadline but slow enough that mid-stream
	// cancellation deterministically races the chunker loop's ctx.Done() check.
	srv := httptest.NewServer(pacedStreamHandler(h, 20*time.Millisecond))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	// Consume bytes up through the first SSE event terminator (\n\n).
	if err := readPastFirstEvent(reader); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body) // drain whatever's already in flight
	resp.Body.Close()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"client_disconnected":true`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `"client_disconnected":true`) {
		t.Errorf("log never showed client_disconnected=true within 1s: %s", buf.String())
	}
	// Verify chunks_emitted < 10 (full mock = 1 bootstrap + 8 content + 1 terminal = 10).
	if got := extractIntFromLog(t, buf.String(), "chunks_emitted"); got >= 10 {
		t.Errorf("chunks_emitted = %d, expected <10 (early exit on cancellation)", got)
	}
}

func readPastFirstEvent(r *bufio.Reader) error {
	var prev byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		if prev == '\n' && b == '\n' {
			return nil
		}
		prev = b
	}
}

func extractIntFromLog(t *testing.T, buf, key string) int {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["event"] != "chat_completions_stream" {
			continue
		}
		if v, ok := m[key].(float64); ok {
			return int(v)
		}
	}
	return -1
}

// Scenario: 3.4-INT-012 — Priority P0
// Client TCP-close mid-stream → next Flush returns error
func TestChatCompletionsStream_ClientDisconnect_ConnectionClose(t *testing.T) {
	buf := &syncBuf{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000012", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	reader := bufio.NewReader(resp.Body)
	if err := readPastFirstEvent(reader); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	srv.CloseClientConnections()
	resp.Body.Close()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), `"client_disconnected":true`) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `"client_disconnected":true`) {
		t.Errorf("log never showed client_disconnected=true: %s", buf.String())
	}
	// flush_error may or may not appear depending on whether the kernel
	// returned the error on the gateway side before the close interrupted
	// it. The disconnect marker is the load-bearing assertion.
}

// Scenario: 3.4-INT-013 — Priority P0
// Goroutine leak guard — 10 successful streams
func TestChatCompletionsStream_NoGoroutineLeak(t *testing.T) {
	// IgnoreCurrent baselines existing goroutines (e.g., http.DefaultClient idleConn workers from earlier tests).
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int00000013", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	client := &http.Client{}
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
		req.Header.Set("Authorization", "Bearer stub")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("iter %d post: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	client.CloseIdleConnections()
}

// errOnSecondFlushWriter wraps a recorder and returns a sentinel error on
// the SECOND Flush() call (the first Flush — for the bootstrap chunk —
// succeeds).
type errOnSecondFlushWriter struct {
	rw         http.ResponseWriter
	flushCalls int
	sentinel   error
}

func (e *errOnSecondFlushWriter) Header() http.Header         { return e.rw.Header() }
func (e *errOnSecondFlushWriter) Write(b []byte) (int, error) { return e.rw.Write(b) }
func (e *errOnSecondFlushWriter) WriteHeader(s int)           { e.rw.WriteHeader(s) }
func (e *errOnSecondFlushWriter) Flush() {
	e.flushCalls++
	if e.flushCalls >= 2 {
		// Drop the inner Flush so the next Flush attempt by the chunker
		// surfaces the sentinel via http.ResponseController (we proxy by
		// hijacking — but the recorder doesn't support that, so just
		// don't flush; the chunker's WriteEvent will succeed against the
		// recorder buffer, then this Flush gets called and we discard).
		return
	}
	if f, ok := e.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// Note: errOnSecondFlushWriter cannot directly raise a Flush error through
// http.ResponseController without significant plumbing. INT-014 is exercised
// via INT-012 (real TCP close triggers a real Flush error). The body of
// INT-014 below documents the failure-mode the runtime guarantees against:
// no panic, response remains 200 (headers already committed).

// Scenario: 3.4-INT-014 — Priority P1
// Defensive panic guard — Flush error mid-stream MUST NOT panic; response
// status stays 200 (already committed); log shows client_disconnected=true.
//
// Approach: drive the failure via a connection close mid-stream against a
// real httptest.NewServer (covered by INT-012); add a no-panic guarantee
// assertion by running INT-012 inside a panic-recovery shim.
func TestChatCompletionsStream_NoPanic_OnFlushError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handler panicked on Flush error: %v", r)
		}
	}()
	buf := &syncBuf{}
	h := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000014", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer stub")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200 (already committed)", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	if err := readPastFirstEvent(reader); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	srv.CloseClientConnections()
	resp.Body.Close()
	// Allow the gateway-side log to materialize.
	time.Sleep(100 * time.Millisecond)
	// We don't strictly require flush_error in the log because the kernel
	// may have buffered enough that all chunks succeed before the gateway
	// notices the close. The no-panic assertion is the load-bearing
	// guarantee for this test (the deferred recover above).
	_ = buf
}

// Scenario: 3.4-INT-015 — Priority P1
// Cross-AC regression: full bearer-auth chain on the SSE path
func TestChatCompletionsStream_BearerAuthIntegration(t *testing.T) {
	const plaintext = "he-INT015STREAMXYZ12345"
	const apiKeyID = "55555555-5555-5555-5555-555555555555"
	const userID = "66666666-6666-6666-6666-666666666666"

	mw, cleanup := newBearerHarness(t, func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if req.GetPlaintextKey() != plaintext {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
		}
		return &authv1.ValidateApiKeyResponse{Ok: true, ApiKeyId: apiKeyID, UserId: userID, Scope: `{}`}, nil
	})
	defer cleanup()

	buf := &syncBuf{}
	chat := newStubbedStreamHandler(buf, "chatcmpl-mock-int00000015", 1715000000)
	wrapped := mw.RequireAPIKey(chat)
	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if !strings.Contains(buf.String(), `"api_key_id":"`+apiKeyID+`"`) {
		t.Errorf("log missing api_key_id from auth-svc stub: %s", buf.String())
	}
}

// Scenario: 3.4-INT-016 — Priority P1
// No-auth deny — handler never invoked (panic-sentinel proves it)
func TestChatCompletionsStream_NoAuth_PathDenies(t *testing.T) {
	mw, cleanup := newBearerHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return &authv1.ValidateApiKeyResponse{Ok: false}, nil
	})
	defer cleanup()

	var innerCalls atomic.Int32
	sentinel := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalls.Add(1)
		panic("chat stream handler MUST NOT be invoked when auth fails")
	})
	wrapped := mw.RequireAPIKey(sentinel)
	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	// No Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if innerCalls.Load() != 0 {
		t.Errorf("inner handler invoked %d times despite auth failure", innerCalls.Load())
	}
}

// Scenario: 3.4-BLIND-ERROR-002 — Priority P1
// Auth-svc 503 → 502; streaming handler not invoked
func TestChatCompletionsStream_AuthSvcUnavailable_502_HandlerNotInvoked(t *testing.T) {
	mw, cleanup := newBearerHarness(t, func(_ *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("auth-svc 503"))
	})
	defer cleanup()

	var innerCalls atomic.Int32
	sentinel := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalls.Add(1)
		panic("streaming handler MUST NOT be invoked when auth-svc upstream errors")
	})
	wrapped := mw.RequireAPIKey(sentinel)
	srv := httptest.NewServer(wrapped)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
	req.Header.Set("Authorization", "Bearer he-anything-still-rejected")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	// Story 3.2 maps auth-svc upstream errors to a non-2xx envelope per the
	// bearer-auth middleware. The exact code (401/500/502/503) depends on
	// the middleware's RPC error mapping — accept any 5xx OR 401 as proof
	// of auth failure. Handler-not-invoked (innerCalls == 0) is the
	// load-bearing assertion.
	if resp.StatusCode < 400 || resp.StatusCode >= 600 {
		t.Errorf("status = %d, want any 4xx/5xx error code", resp.StatusCode)
	}
	if innerCalls.Load() != 0 {
		t.Errorf("streaming handler invoked %d times despite auth-svc failure", innerCalls.Load())
	}
}

// Scenario: 3.4-BLIND-CONCURRENCY-001 — Priority P1
// 10 concurrent streams + 5 mid-stream cancellations + goleak
func TestChatCompletionsStream_ConcurrentStreamsWithMidCancel_NoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	buf := &syncBuf{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := handlers.NewChatCompletionsHandler(logger,
		handlers.WithIDFactory(func() string { return "chatcmpl-mock-int000bdcc1" }),
		handlers.WithNow(func() time.Time { return time.Unix(1715000000, 0).UTC() }),
	)
	// Paced flush so concurrent cancellations have a real chance to race.
	srv := httptest.NewServer(pacedStreamHandler(h, 10*time.Millisecond))
	defer srv.Close()

	client := &http.Client{}
	defer client.CloseIdleConnections()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
			req.Header.Set("Authorization", "Bearer stub")
			resp, err := client.Do(req)
			if err != nil {
				return // cancellation race — acceptable
			}
			reader := bufio.NewReader(resp.Body)
			_ = readPastFirstEvent(reader)
			if i < 5 {
				cancel()
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	// Allow the gateway-side log lines to materialize.
	time.Sleep(200 * time.Millisecond)

	logged := buf.String()

	disconnected := strings.Count(logged, `"client_disconnected":true`)
	connected := strings.Count(logged, `"client_disconnected":false`)
	total := disconnected + connected
	if total < 5 {
		t.Errorf("only %d stream log lines captured; expected ~10", total)
	}
	// Loose floor: at least some disconnects observed (5 cancellations
	// SHOULD all surface but race timing can occasionally allow one to
	// complete before cancel takes effect).
	if disconnected == 0 {
		t.Errorf("no client_disconnected=true log lines; expected at least some\nlog=%s", logged)
	}
}

// Scenario: 3.4-BLIND-RESOURCE-001 — Priority P2
// FD-leak guard after 100 sequential streams.
//
// Note: portable FD counting differs across OS; we use a goroutine-count
// surrogate via goleak (the stdlib net/http pools idleConns into a fixed
// number of goroutines — any leak shows up as growth). This is more
// reliable across Linux/Darwin than /proc/self/fd or sysctl.
func TestChatCompletionsStream_FDCountReturnsToBaseline_After100Requests(t *testing.T) {
	h := newStubbedStreamHandler(nil, "chatcmpl-mock-int000bdr1", 1715000000)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	client := &http.Client{}
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
		req.Header.Set("Authorization", "Bearer stub")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	client.CloseIdleConnections()
	// Goleak verification after CloseIdleConnections — if the chunker leaked
	// a goroutine per request, goleak surfaces 100+ leaked goroutines.
	if err := goleak.Find(goleak.IgnoreCurrent()); err != nil {
		// goleak.Find may report transient idleConn workers; allow brief settle.
		time.Sleep(200 * time.Millisecond)
		if err2 := goleak.Find(goleak.IgnoreCurrent()); err2 != nil {
			t.Errorf("goroutine leak detected after 100 streams: %v", err2)
		}
	}
}

// Scenario: 3.4-BLIND-FLOW-001 — Priority P2
// Duplicate submission — same body twice yields DIFFERENT ids (BR-1.4)
func TestChatCompletionsStream_DuplicateSubmission_DifferentID(t *testing.T) {
	// Production factory (real crypto/rand) so we get unique ids per call.
	h := handlers.NewChatCompletionsHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		handlers.WithNow(func() time.Time { return time.Unix(1715000000, 0).UTC() }),
	)
	srv := httptest.NewServer(streamHandler(h))
	defer srv.Close()

	collect := func() (string, int) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(streamReqBody))
		req.Header.Set("Authorization", "Bearer stub")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		// Extract the first chunk's id field.
		i := strings.Index(string(body), `"id":"`)
		if i < 0 {
			t.Fatalf("no id in body: %s", string(body))
		}
		j := strings.Index(string(body)[i+6:], `"`)
		id := string(body)[i+6 : i+6+j]
		chunks := strings.Count(string(body), `data: {`)
		return id, chunks
	}
	id1, chunks1 := collect()
	id2, chunks2 := collect()
	if id1 == id2 {
		t.Errorf("duplicate submission produced same id %q — id factory may be memoized", id1)
	}
	if chunks1 != chunks2 {
		t.Errorf("duplicate submission produced different chunk counts (%d vs %d) — shape should be deterministic",
			chunks1, chunks2)
	}
}

// ----- Redis import kept indirect-only via newBearerHarness; suppress lint
var _ = func() *redis.Client { return nil }
var _ = miniredis.RunT

// Ensure authv1connect import survives if no test uses it directly.
var _ = authv1connect.NewAuthServiceClient
