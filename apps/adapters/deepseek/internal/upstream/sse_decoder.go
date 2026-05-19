package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrMalformedFrame is returned by Decoder.NextChunk on a frame that does
// not conform to the strict-RFC `data: <json>\n\n` shape ratified by OQ6
// (Architect Round 2). Silent coercion masks vendor protocol drift —
// surfacing the error lets the gateway map to 502 + the operator opens a
// vendor-quirk ticket per BR-2.8.
var ErrMalformedFrame = errors.New("sse decoder: malformed frame (strict-RFC violation)")

// sseMaxLineBytes caps a single SSE line at 1 MiB to guard against a
// pathological upstream emitting an unbounded line. Matches the proto
// MaxRecvMsgSize default.
const sseMaxLineBytes = 1 << 20

// Strict-RFC accepted prefix. Must be exactly `data: ` (six bytes — field
// name + colon + single space). Any deviation (`data:`, `event: `, comments,
// etc.) is malformed per OQ6.
var (
	sseDataPrefix = []byte("data: ")
	sseDoneBody   = []byte("[DONE]")
)

// Decoder reads OpenAI-compatible SSE frames from an io.Reader and decodes
// them into ChatChunkJSON. Strict-RFC per OQ6:
//
//	(a) `data: <json>\n\n` is the only accepted frame.
//	(b) `data: [DONE]\n\n` terminates the stream → NextChunk returns
//	    (nil, io.EOF).
//	(c) Any other line prefix (`event:`, `id:`, `retry:`, comments, bare
//	    text, `data:` without space) is treated as malformed →
//	    ErrMalformedFrame. The gateway surfaces 502; vendor-quirk drift
//	    opens a follow-up bug per BR-2.8 (no silent coercion).
//	(d) CRLF line terminators are malformed — strict-LF only.
//
// Decoder is NOT safe for concurrent use; one Decoder per upstream HTTPS
// response body.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder wraps an io.Reader for SSE frame decoding. The caller retains
// ownership of r (typically the upstream *http.Response.Body).
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 256<<10), sseMaxLineBytes)
	s.Split(scanLFOnly)
	return &Decoder{scanner: s}
}

// NextChunk reads the next SSE frame and returns the decoded ChatChunkJSON.
// The context is checked between scanner reads so cancellation cascades
// cleanly when the gateway / client disconnects mid-stream.
//
// Returns:
//
//	(chunk, nil) on a successful frame.
//	(nil, io.EOF) on the `data: [DONE]` terminator OR clean stream-end
//	  without a [DONE] (some upstreams close mid-stream — surface as EOF;
//	  the adapter's terminal-chunk-required logic catches missing-usage
//	  via BR-3.4, NOT here).
//	(nil, ErrMalformedFrame) on a strict-RFC violation.
//	(nil, err) on scanner I/O failure or ctx cancellation.
func (d *Decoder) NextChunk(ctx context.Context) (*ChatChunkJSON, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !d.scanner.Scan() {
			if err := d.scanner.Err(); err != nil {
				return nil, fmt.Errorf("sse decoder scan: %w", err)
			}
			return nil, io.EOF
		}
		line := d.scanner.Bytes()
		// Strict-LF: any CR in the line means the upstream used CRLF
		// terminators. Reject per BR-2.8 — silent coercion masks vendor
		// protocol drift.
		if bytes.IndexByte(line, '\r') >= 0 {
			return nil, ErrMalformedFrame
		}
		if len(line) == 0 {
			// Blank line between events — frame boundary; loop for next line.
			continue
		}
		if !bytes.HasPrefix(line, sseDataPrefix) {
			return nil, ErrMalformedFrame
		}
		payload := line[len(sseDataPrefix):]
		if bytes.Equal(payload, sseDoneBody) {
			return nil, io.EOF
		}
		chunk := &ChatChunkJSON{}
		if err := json.Unmarshal(payload, chunk); err != nil {
			return nil, ErrMalformedFrame
		}
		return chunk, nil
	}
}

// scanLFOnly is a bufio.Scanner SplitFunc that splits strictly on `\n`. It
// does NOT strip a leading `\r` (unlike bufio.ScanLines), so CRLF inputs
// surface a trailing `\r` in the token that the decoder rejects as malformed.
func scanLFOnly(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[0:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
