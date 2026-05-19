package upstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Scenario: 4.1-UNIT-021 — strict-RFC standard frame decoded.
func TestSSEDecoder_StandardFrame(t *testing.T) {
	t.Parallel()
	body := "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"},\"finish_reason\":null}]}\n\n"
	d := NewDecoder(strings.NewReader(body))
	chunk, err := d.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("NextChunk err: %v", err)
	}
	if chunk.ID != "chatcmpl-abc" || chunk.Object != "chat.completion.chunk" || chunk.Model != "deepseek-v3" {
		t.Fatalf("unexpected chunk shape: %+v", chunk)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Delta == nil || chunk.Choices[0].Delta.Content == nil || *chunk.Choices[0].Delta.Content != "Hi" {
		t.Fatalf("unexpected delta: %+v", chunk.Choices)
	}
}

// Scenario: 4.1-UNIT-022 — `data: [DONE]\n\n` terminator returns io.EOF.
func TestSSEDecoder_DoneReturnsEOF(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("data: [DONE]\n\n"))
	chunk, err := d.NextChunk(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got err=%v chunk=%v", err, chunk)
	}
	if chunk != nil {
		t.Fatalf("expected nil chunk on [DONE], got %+v", chunk)
	}
}

// Scenario: 4.1-UNIT-023 — strict-RFC: event: prefix triggers ErrMalformedFrame (OQ6).
func TestSSEDecoder_NonDataPrefixIsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"event_prefix", "event: ping\n\n"},
		{"id_prefix", "id: 42\n\n"},
		{"comment_prefix", ": keep-alive\n\n"},
		{"retry_prefix", "retry: 5000\n\n"},
		{"bare_text", "ping\n\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := NewDecoder(strings.NewReader(tc.body))
			_, err := d.NextChunk(context.Background())
			if !errors.Is(err, ErrMalformedFrame) {
				t.Fatalf("expected ErrMalformedFrame, got %v", err)
			}
		})
	}
}

// Scenario: 4.1-UNIT-024 — strict-RFC: missing space after data: → malformed.
func TestSSEDecoder_MissingSpaceAfterDataIsMalformed(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("data:{\"id\":\"x\"}\n\n"))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("expected ErrMalformedFrame for data: without space, got %v", err)
	}
}

// Scenario: 4.1-UNIT-024b — strict-RFC: malformed JSON payload → ErrMalformedFrame.
func TestSSEDecoder_MalformedJSONIsMalformed(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("data: {not json\n\n"))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("expected ErrMalformedFrame for invalid JSON, got %v", err)
	}
}

// Scenario: 4.1-UNIT-025 — strict-RFC: CRLF line terminators → malformed (BR-2.8).
func TestSSEDecoder_CRLFIsMalformed(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("data: {\"id\":\"x\"}\r\n\r\n"))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("expected ErrMalformedFrame for CRLF terminator, got %v", err)
	}
}

// Scenario: 4.1-UNIT-025b — terminal usage chunk decoded with usage populated.
func TestSSEDecoder_TerminalChunkWithUsage(t *testing.T) {
	t.Parallel()
	body := "data: {\"id\":\"chatcmpl-zzz\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"deepseek-v3\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n"
	d := NewDecoder(strings.NewReader(body))
	chunk, err := d.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("NextChunk err: %v", err)
	}
	if chunk.Usage == nil {
		t.Fatalf("expected Usage populated on terminal chunk")
	}
	if chunk.Usage.PromptTokens != 3 || chunk.Usage.CompletionTokens != 7 || chunk.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected usage: %+v", chunk.Usage)
	}
}

// Scenario: 4.1-UNIT-025c — clean EOF without [DONE] returns io.EOF (the
// adapter's terminal-chunk-required logic catches missing-usage upstream).
func TestSSEDecoder_CleanEOFNoDone(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader(""))
	_, err := d.NextChunk(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF on empty body, got %v", err)
	}
}

// Scenario: 4.1-UNIT-025d — context cancellation between frames returns
// the context error (cascades cleanly when the client disconnects mid-stream).
func TestSSEDecoder_ContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := NewDecoder(strings.NewReader("data: {\"id\":\"x\"}\n\n"))
	_, err := d.NextChunk(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// Scenario: 4.1-UNIT-025e — multi-frame stream decoded sequentially.
func TestSSEDecoder_MultiFrame(t *testing.T) {
	t.Parallel()
	body := strings.Join([]string{
		"data: {\"id\":\"chatcmpl-multi\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}",
		"",
		"data: {\"id\":\"chatcmpl-multi\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}",
		"",
		"data: {\"id\":\"chatcmpl-multi\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}",
		"",
		"data: {\"id\":\"chatcmpl-multi\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"deepseek-v3\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}",
		"",
		"data: [DONE]",
		"",
		"",
	}, "\n")
	d := NewDecoder(strings.NewReader(body))
	var got []ChatChunkJSON
	ctx := context.Background()
	for {
		c, err := d.NextChunk(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got = append(got, *c)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 chunks before [DONE], got %d", len(got))
	}
	if got[3].Usage == nil || got[3].Usage.TotalTokens != 6 {
		t.Fatalf("expected terminal usage chunk last, got %+v", got[3])
	}
}
