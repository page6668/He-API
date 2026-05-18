// Story 3.4 — Generic SSE writer abstraction for OpenAI-compatible streaming.
//
// Documents the W3C EventSource §9.2.6 event-framing rules (each event is
// rendered as `data: <single-line JSON>\n\n` — double-LF terminator) plus
// the OpenAI extension `data: [DONE]\n\n` that signals iterator termination
// to the official Python/Node/Go SDKs.
//
// The package is the first occupant of the `apps/api-gateway/internal/streaming/`
// slot reserved by source-tree.md §6 since Epic 1. Story 3.5+ and Epic 4
// streaming adapters MUST reuse this Writer surface (BR-4.4 minimal interface);
// new observability dimensions (TTFB, per-byte counters, span events) live in
// the chunker / handler — NOT bolted onto the Writer (Architect Round 1 AR1-R4).
//
// Per BR-1.10, Flush() routes through http.ResponseController (Go 1.20+) so it
// survives middleware-wrapped ResponseWriters (obs.WrapHTTPHandler at Story 1.5).
package streaming

import (
	"errors"
	"net/http"
)

// ErrFlushUnsupported is returned by Writer.Flush when the underlying
// ResponseWriter does not support flushing (neither http.Flusher nor
// http.ResponseController can reach a Flusher in the wrapper chain).
// Callers may errors.Is to detect this case (BR-1.10).
var ErrFlushUnsupported = errors.New("streaming: ResponseWriter does not support flush")

// Writer is the minimal SSE writer interface. Implementations wrap an
// http.ResponseWriter and serialize events per W3C EventSource §9.2.6.
// BR-4.4 keeps the surface small + stable; observability extensions
// (TTFB, span events) belong in the chunker, not here.
type Writer interface {
	// WriteEvent emits a single SSE event. The `event` argument is "" for
	// unnamed `data: ...` events (the only kind Story 3.4 uses); future
	// Stories MAY pass a non-empty name to emit `event: <name>\ndata: ...`.
	WriteEvent(event string, data []byte) error

	// WriteDone emits the literal bytes `data: [DONE]\n\n` and flushes.
	// Dedicated method per Architect Round 1 AR1-R3 — defends against a
	// future regression where a generic WriteEvent path accidentally
	// JSON-quotes the body and silently breaks SDK iterators.
	WriteDone() error

	// Flush forces buffered bytes to the underlying connection. Returns
	// ErrFlushUnsupported (joined with the underlying error if any) when
	// the writer chain has no Flusher.
	Flush() error

	// Close releases any per-stream resources. For Story 3.4 this is a
	// no-op (the HTTP framework owns connection lifecycle); future
	// streaming Writers MAY hold resources (e.g., per-stream span ctx).
	Close() error
}

// writer is the concrete SSE Writer over an http.ResponseWriter. Use
// NewWriter to construct.
type writer struct {
	w              http.ResponseWriter
	rc             *http.ResponseController
	headersWritten bool
}

// NewWriter constructs a Writer over w. Headers are written lazily on the
// first WriteEvent / WriteDone — callers may inspect/modify w.Header() up
// until the first emit.
func NewWriter(w http.ResponseWriter) Writer {
	return &writer{w: w, rc: http.NewResponseController(w)}
}

// AC1 GIVEN/THEN response header bundle (BR-1.1):
//   - Content-Type: text/event-stream; charset=utf-8
//   - Cache-Control: no-cache, no-transform
//   - Connection: keep-alive
//   - X-Accel-Buffering: no  (defence against nginx-style proxy buffering)
func (s *writer) writeHeadersOnce() {
	if s.headersWritten {
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	s.w.WriteHeader(http.StatusOK)
	s.headersWritten = true
}

func (s *writer) WriteEvent(event string, data []byte) error {
	s.writeHeadersOnce()
	// Pre-size: `event: ` + name + `\n` + `data: ` + payload + `\n\n`.
	bufLen := len("data: ") + len(data) + 2
	if event != "" {
		bufLen += len("event: ") + len(event) + 1
	}
	buf := make([]byte, 0, bufLen)
	if event != "" {
		buf = append(buf, "event: "...)
		buf = append(buf, event...)
		buf = append(buf, '\n')
	}
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, '\n', '\n')
	_, err := s.w.Write(buf)
	return err
}

func (s *writer) WriteDone() error {
	s.writeHeadersOnce()
	if _, err := s.w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	return s.Flush()
}

func (s *writer) Flush() error {
	err := s.rc.Flush()
	if err == nil {
		return nil
	}
	if errors.Is(err, http.ErrNotSupported) {
		return errors.Join(ErrFlushUnsupported, err)
	}
	return err
}

func (s *writer) Close() error { return nil }
