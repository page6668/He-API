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
// (Architect Round 2, Story 4.1; inherited by Stories 4.2/4.3/4.4/4.5/4.6
// per L1 simplification — Qianfan v2 OpenAI-compat SSE matches OpenAI
// byte-for-byte, no Baidu-native heartbeat carve-out per OQ-4.6-4).
var ErrMalformedFrame = errors.New("sse decoder: malformed frame (strict-RFC violation)")

// sseMaxLineBytes caps a single SSE line at 1 MiB.
const sseMaxLineBytes = 1 << 20

var (
	sseDataPrefix = []byte("data: ")
	sseDoneBody   = []byte("[DONE]")
)

// Decoder reads OpenAI-compatible SSE frames (Qianfan v2 API emits the
// OpenAI shape byte-for-byte) and decodes them into ChatChunkJSON.
// Strict-RFC per OQ6:
//
//	(a) `data: <json>\n\n` is the only accepted frame.
//	(b) `data: [DONE]\n\n` terminates the stream → NextChunk returns
//	    (nil, io.EOF).
//	(c) Any other line prefix is malformed → ErrMalformedFrame.
//	(d) CRLF line terminators are malformed — strict-LF only.
//
// Decoder is NOT safe for concurrent use.
type Decoder struct {
	scanner *bufio.Scanner
}

// NewDecoder wraps an io.Reader for SSE frame decoding.
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 256<<10), sseMaxLineBytes)
	s.Split(scanLFOnly)
	return &Decoder{scanner: s}
}

// NextChunk reads the next SSE frame and returns the decoded ChatChunkJSON.
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
		if bytes.IndexByte(line, '\r') >= 0 {
			return nil, ErrMalformedFrame
		}
		if len(line) == 0 {
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

// scanLFOnly is a bufio.Scanner SplitFunc that splits strictly on `\n`.
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
