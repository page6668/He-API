package upstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// 4.2-UNIT-008 (P0) — BR-2.3 strict-RFC SSE decode (compat-mode shape).
// Decoder MUST decode `data: <json>\n\n` frames into ChatChunkJSON.
func TestDecoder_NextChunk_DecodesValidFrame(t *testing.T) {
	stream := "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"qwen-max\",\"choices\":[]}\n\n"
	d := NewDecoder(strings.NewReader(stream))
	chunk, err := d.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("NextChunk err = %v", err)
	}
	if chunk.ID != "x" || chunk.Object != "chat.completion.chunk" || chunk.Model != "qwen-max" {
		t.Fatalf("decoded chunk mismatch: %#v", chunk)
	}
}

// 4.2-UNIT-008b — `data: [DONE]\n\n` terminator → io.EOF.
func TestDecoder_NextChunk_DoneTerminator_ReturnsEOF(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: [DONE]\n\n"))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("NextChunk on [DONE] err = %v, want io.EOF", err)
	}
}

// 4.2-UNIT-008c — malformed frame → ErrMalformedFrame.
func TestDecoder_NextChunk_MalformedFrame_ReturnsErr(t *testing.T) {
	cases := []string{
		"event: ping\n\n",
		"data:no-space-after-colon\n\n",
		"id: 1\n\n",
		"data: not-json\n\n",
	}
	for _, s := range cases {
		d := NewDecoder(strings.NewReader(s))
		_, err := d.NextChunk(context.Background())
		if !errors.Is(err, ErrMalformedFrame) {
			t.Fatalf("input %q: NextChunk err = %v, want ErrMalformedFrame", s, err)
		}
	}
}

// 4.2-UNIT-008d — CRLF terminator → ErrMalformedFrame (strict-LF only).
func TestDecoder_NextChunk_CRLF_IsMalformed(t *testing.T) {
	d := NewDecoder(strings.NewReader("data: {}\r\n\r\n"))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("CRLF input err = %v, want ErrMalformedFrame", err)
	}
}

// 4.2-UNIT-008e — blank lines between events tolerated as frame
// boundaries; next frame decoded normally.
func TestDecoder_NextChunk_MultipleFrames(t *testing.T) {
	stream := "data: {\"id\":\"a\"}\n\n" +
		"data: {\"id\":\"b\"}\n\n" +
		"data: [DONE]\n\n"
	d := NewDecoder(strings.NewReader(stream))
	first, err := d.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("first NextChunk err = %v", err)
	}
	if first.ID != "a" {
		t.Fatalf("first.ID = %q, want a", first.ID)
	}
	second, err := d.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("second NextChunk err = %v", err)
	}
	if second.ID != "b" {
		t.Fatalf("second.ID = %q, want b", second.ID)
	}
	if _, err := d.NextChunk(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("third NextChunk (post-DONE) err = %v, want io.EOF", err)
	}
}

// 4.2-UNIT-008f — context cancellation propagates.
func TestDecoder_NextChunk_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := NewDecoder(strings.NewReader("data: {\"id\":\"x\"}\n\n"))
	_, err := d.NextChunk(ctx)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("NextChunk after cancel err = %v, want context.Canceled", err)
	}
}
