// Story 3.4 — Writer abstraction tests (sse.go).
//
// Test Design: docs/qa/assessments/3.4-test-design-20260518.md
// Architect Round 1 Rulings honored: AR1-R3 (dedicated WriteDone), BR-1.10 (ResponseController)

package streaming

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ============================================================
// AC1.A — Writer Wire-Format Primitives (Unit)
// ============================================================

// Scenario: 3.4-UNIT-001 — Priority P0 — Level unit
// BR-1.2: data: <json>\n\n byte-exact framing (load-bearing for OpenAI SDK iterator)
func TestWriter_WriteEvent_FormatsAsDataLineWithDoubleLF(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.WriteEvent("", []byte(`{"x":1}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	got := rr.Body.String()
	want := "data: {\"x\":1}\n\n"
	if got != want {
		t.Errorf("body byte-mismatch\n got=%q\nwant=%q", got, want)
	}
}

// Scenario: 3.4-UNIT-002 — Priority P0 — Level unit
// BR-4.4: named-event variant interface guarantee
func TestWriter_WriteEvent_NamedEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.WriteEvent("error", []byte(`{"e":"x"}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	got := rr.Body.String()
	want := "event: error\ndata: {\"e\":\"x\"}\n\n"
	if got != want {
		t.Errorf("body byte-mismatch\n got=%q\nwant=%q", got, want)
	}
}

// Scenario: 3.4-UNIT-003 — Priority P0 — Level unit
// BR-4.5 + AR1-R3: WriteDone() emits literal "data: [DONE]\n\n" byte-exact
func TestWriter_WriteDone_EmitsSentinelByteExact(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.WriteDone(); err != nil {
		t.Fatalf("WriteDone: %v", err)
	}
	got := rr.Body.String()
	want := "data: [DONE]\n\n"
	if got != want {
		t.Errorf("sentinel byte-mismatch\n got=%q\nwant=%q", got, want)
	}
}

// Scenario: 3.4-UNIT-004 — Priority P0 — Level unit
// BR-1.1: AC1 GIVEN/THEN response headers set BEFORE first Flush()
func TestWriter_NewWriter_SetsHeadersBeforeFirstFlush(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.WriteEvent("", []byte(`{}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	wantHeaders := map[string]string{
		"Content-Type":      "text/event-stream; charset=utf-8",
		"Cache-Control":     "no-cache, no-transform",
		"Connection":        "keep-alive",
		"X-Accel-Buffering": "no",
	}
	for k, want := range wantHeaders {
		if got := rr.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// nonFlusherWriter wraps a recorder but does NOT advertise http.Flusher
// directly. http.ResponseController must walk the wrapper chain via the
// (interface{ Unwrap() http.ResponseWriter }) convention to find the
// underlying Flusher.
type nonFlusherWriter struct{ rw http.ResponseWriter }

func (n *nonFlusherWriter) Header() http.Header         { return n.rw.Header() }
func (n *nonFlusherWriter) Write(b []byte) (int, error) { return n.rw.Write(b) }
func (n *nonFlusherWriter) WriteHeader(s int)           { n.rw.WriteHeader(s) }
func (n *nonFlusherWriter) Unwrap() http.ResponseWriter { return n.rw }

// Scenario: 3.4-UNIT-005 — Priority P0 — Level unit
// BR-1.10: http.ResponseController survives middleware-wrapped writers
func TestWriter_Flush_UsesResponseController(t *testing.T) {
	rr := httptest.NewRecorder()
	wrap := &nonFlusherWriter{rw: rr}
	if _, ok := http.ResponseWriter(wrap).(http.Flusher); ok {
		t.Fatal("test premise broken: nonFlusherWriter directly implements http.Flusher")
	}
	w := NewWriter(wrap)
	if err := w.WriteEvent("", []byte(`{}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush via ResponseController failed: %v", err)
	}
}

// nonFlushableWriter has no Flusher AND no Unwrap path — ResponseController
// cannot reach a Flusher anywhere in the chain.
type nonFlushableWriter struct {
	buf    *bytes.Buffer
	header http.Header
	status int
}

func (n *nonFlushableWriter) Header() http.Header         { return n.header }
func (n *nonFlushableWriter) Write(b []byte) (int, error) { return n.buf.Write(b) }
func (n *nonFlushableWriter) WriteHeader(s int)           { n.status = s }

// Scenario: 3.4-UNIT-006 — Priority P0 — Level unit
// BR-1.10: defensive contract — caller-detectable failure via errors.Is
func TestWriter_Flush_ReturnsErrorOnNonFlushableWriter(t *testing.T) {
	nf := &nonFlushableWriter{buf: &bytes.Buffer{}, header: http.Header{}}
	w := NewWriter(nf)
	if err := w.WriteEvent("", []byte(`{}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	err := w.Flush()
	if err == nil {
		t.Fatal("Flush succeeded; expected ErrFlushUnsupported")
	}
	if !errors.Is(err, ErrFlushUnsupported) {
		t.Errorf("Flush err = %v; expected errors.Is(err, ErrFlushUnsupported) == true", err)
	}
}

// Scenario: 3.4-UNIT-007 — Priority P1 — Level unit
// BR-4.4: Close() is a no-op for this Story (no per-stream resources)
func TestWriter_Close_NoOp(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.Close(); err != nil {
		t.Errorf("Close returned %v; want nil", err)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("Close wrote body bytes: %q", rr.Body.String())
	}
}

// Scenario: 3.4-UNIT-008 — Priority P0 — Level unit
// Headers-written-once invariant; guards against future per-chunk-loop Header().Set() regression
func TestWriter_HeadersWrittenOnce(t *testing.T) {
	rr := httptest.NewRecorder()
	w := NewWriter(rr)
	if err := w.WriteEvent("", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("WriteEvent 1: %v", err)
	}
	// Mutate header to a sentinel; a future regression that re-Sets headers
	// on the second WriteEvent would overwrite this sentinel.
	rr.Header().Set("X-Sentinel-Should-Survive", "yes")
	if err := w.WriteEvent("", []byte(`{"a":2}`)); err != nil {
		t.Fatalf("WriteEvent 2: %v", err)
	}
	if got := rr.Header().Get("X-Sentinel-Should-Survive"); got != "yes" {
		t.Errorf("headers re-written on 2nd WriteEvent (sentinel lost): %q", got)
	}
	want := "data: {\"a\":1}\n\ndata: {\"a\":2}\n\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body byte-mismatch\n got=%q\nwant=%q", got, want)
	}
}

// ----- Blind-spot overlay --------------------------------------------------

// closeErrWriter wraps Writer to inject a Close() error; used by the
// defensive ERROR-005 scenario.
type closeErrWriter struct {
	inner   Writer
	closeFn func() error
}

func (c *closeErrWriter) WriteEvent(event string, data []byte) error {
	return c.inner.WriteEvent(event, data)
}
func (c *closeErrWriter) WriteDone() error { return c.inner.WriteDone() }
func (c *closeErrWriter) Flush() error     { return c.inner.Flush() }
func (c *closeErrWriter) Close() error     { return c.closeFn() }

// Scenario: 3.4-BLIND-ERROR-001 — Priority P2 — Level unit
// [BLIND-SPOT ERROR-005] — defensive for future Writer impls that hold per-stream resources
func TestWriter_Close_FailurePath_LogsWarnAndDoesNotPanic(t *testing.T) {
	rr := httptest.NewRecorder()
	inner := NewWriter(rr)
	w := &closeErrWriter{
		inner:   inner,
		closeFn: func() error { return errors.New("simulated close failure") },
	}
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Close panicked: %v", r)
		}
	}()
	if err := w.WriteEvent("", []byte(`{}`)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Close(); err != nil {
		logger.Warn("sse_writer_close_failed",
			slog.String("event", "sse_writer_close_failed"),
			slog.String("error", err.Error()))
	}
	logged := buf.String()
	if !strings.Contains(logged, `"event":"sse_writer_close_failed"`) {
		t.Errorf("warn log missing event marker: %s", logged)
	}
	if !strings.Contains(logged, `simulated close failure`) {
		t.Errorf("warn log missing error string: %s", logged)
	}
}
