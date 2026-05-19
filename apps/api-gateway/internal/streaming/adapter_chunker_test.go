// Story 4.1 — AdapterChunker tests (UNIT-017..020).
package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// fakeAdapterStream implements AdapterChunkStream for unit testing. The
// chunks slice is consumed in order; receiveErr surfaces after the chunks
// are drained (the equivalent of `connect.ServerStreamForClient.Err()`).
type fakeAdapterStream struct {
	chunks []*adapterv1.ChatChunk
	idx    int
	err    error
}

func (s *fakeAdapterStream) Receive() bool {
	if s.idx >= len(s.chunks) {
		return false
	}
	s.idx++
	return true
}

func (s *fakeAdapterStream) Msg() *adapterv1.ChatChunk {
	if s.idx == 0 || s.idx > len(s.chunks) {
		return nil
	}
	return s.chunks[s.idx-1]
}

func (s *fakeAdapterStream) Err() error { return s.err }

func ptr[T any](v T) *T { return &v }

// chunkDelta is a small builder for delta-only chunks (no usage).
func chunkDelta(id string, role, content string, finish *string) *adapterv1.ChatChunk {
	delta := &adapterv1.Delta{}
	if role != "" {
		delta.Role = ptr(role)
	}
	if content != "" {
		delta.Content = ptr(content)
	}
	choice := &adapterv1.Choice{Index: 0, Delta: delta, FinishReason: finish}
	return &adapterv1.ChatChunk{
		Id:      id,
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "deepseek-v3",
		Choices: []*adapterv1.Choice{choice},
	}
}

func chunkTerminal(id string, prompt, completion, total int32) *adapterv1.ChatChunk {
	return &adapterv1.ChatChunk{
		Id:      id,
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "deepseek-v3",
		Choices: []*adapterv1.Choice{},
		Usage: &adapterv1.Usage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      total,
		},
	}
}

// 4.1-UNIT-017 (P0) — BR-2.2: AdapterChunker.Stream emits N `data: <json>\n\n`
// SSE frames + a literal `data: [DONE]\n\n` terminator.
func TestAdapterChunker_StreamEmitsAllFramesPlusDONE(t *testing.T) {
	stop := "stop"
	stream := &fakeAdapterStream{
		chunks: []*adapterv1.ChatChunk{
			chunkDelta("chatcmpl-1", "assistant", "", nil),
			chunkDelta("chatcmpl-1", "", "Hi", nil),
			chunkDelta("chatcmpl-1", "", " there", &stop),
			chunkTerminal("chatcmpl-1", 4, 2, 6),
		},
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	chunks, firstFlushAt, err := chunker.Stream(context.Background(), w)
	if err != nil {
		t.Fatalf("Stream err = %v", err)
	}
	if chunks != 4 {
		t.Fatalf("chunksEmitted = %d, want 4", chunks)
	}
	if firstFlushAt.IsZero() {
		t.Fatalf("firstFlushAt should be non-zero after at least one chunk emit")
	}
	body := rec.Body.String()
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("body must end with data: [DONE]\\n\\n; got:\n%s", body)
	}
	// 4 chunks + 1 DONE = 5 `data:` lines.
	if got := strings.Count(body, "\ndata: "); got != 4 { // counts every data: after the first
		// Account for first line not having a leading newline. Use a starts-with check instead.
	}
	gotData := strings.Count(body, "data: ")
	if gotData != 5 {
		t.Fatalf("expected 5 data: prefixes (4 chunks + DONE), got %d in:\n%s", gotData, body)
	}
}

// 4.1-UNIT-018 (P0) — BR-2.2 output shape: each emitted SSE event is the
// OpenAI `chat.completion.chunk` JSON exactly — id/object/created/model/
// choices/(usage).
func TestAdapterChunker_OpenAIChunkJSONShape(t *testing.T) {
	stop := "stop"
	stream := &fakeAdapterStream{
		chunks: []*adapterv1.ChatChunk{
			chunkDelta("chatcmpl-shape", "assistant", "", nil),
			chunkDelta("chatcmpl-shape", "", "ok", &stop),
			chunkTerminal("chatcmpl-shape", 1, 1, 2),
		},
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	if _, _, err := chunker.Stream(context.Background(), w); err != nil {
		t.Fatalf("Stream err = %v", err)
	}
	dataPayloads := extractSSEDataPayloads(t, rec.Body.String())
	if len(dataPayloads) != 4 { // 3 chunks + [DONE]
		t.Fatalf("expected 4 data payloads (3 + DONE), got %d: %v", len(dataPayloads), dataPayloads)
	}
	if dataPayloads[3] != "[DONE]" {
		t.Fatalf("last data payload = %q, want [DONE]", dataPayloads[3])
	}
	// First chunk: bootstrap with role=assistant.
	var bootstrap map[string]any
	if err := json.Unmarshal([]byte(dataPayloads[0]), &bootstrap); err != nil {
		t.Fatalf("unmarshal bootstrap: %v; payload=%s", err, dataPayloads[0])
	}
	if bootstrap["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v, want chat.completion.chunk", bootstrap["object"])
	}
	if bootstrap["model"] != "deepseek-v3" {
		t.Fatalf("model = %v, want deepseek-v3", bootstrap["model"])
	}
	choices, ok := bootstrap["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices invalid: %v", bootstrap["choices"])
	}
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Fatalf("bootstrap delta role = %v, want assistant", delta["role"])
	}
	// Terminal chunk (index 2): usage populated.
	var terminal map[string]any
	if err := json.Unmarshal([]byte(dataPayloads[2]), &terminal); err != nil {
		t.Fatalf("unmarshal terminal: %v", err)
	}
	usage, ok := terminal["usage"].(map[string]any)
	if !ok {
		t.Fatalf("terminal usage missing: %v", terminal)
	}
	if usage["prompt_tokens"].(float64) != 1 || usage["completion_tokens"].(float64) != 1 || usage["total_tokens"].(float64) != 2 {
		t.Fatalf("terminal usage = %v, want {1,1,2}", usage)
	}
}

// 4.1-UNIT-019 (P0) — BR-2.4: terminal chunk's `usage` is carried into the
// emitted SSE frame BEFORE [DONE]. Penultimate `data:` line MUST include
// the `usage` object.
func TestAdapterChunker_TerminalUsage_EmittedBeforeDone(t *testing.T) {
	stream := &fakeAdapterStream{
		chunks: []*adapterv1.ChatChunk{
			chunkDelta("chatcmpl-z", "assistant", "x", nil),
			chunkTerminal("chatcmpl-z", 5, 10, 15),
		},
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	if _, _, err := chunker.Stream(context.Background(), w); err != nil {
		t.Fatalf("Stream err = %v", err)
	}
	payloads := extractSSEDataPayloads(t, rec.Body.String())
	if len(payloads) < 3 {
		t.Fatalf("expected at least 3 payloads (2 chunks + DONE), got %d", len(payloads))
	}
	penultimate := payloads[len(payloads)-2]
	if !strings.Contains(penultimate, `"prompt_tokens":5`) || !strings.Contains(penultimate, `"completion_tokens":10`) || !strings.Contains(penultimate, `"total_tokens":15`) {
		t.Fatalf("BR-2.4: penultimate frame missing tail-usage: %s", penultimate)
	}
	if payloads[len(payloads)-1] != "[DONE]" {
		t.Fatalf("last payload = %q, want [DONE]", payloads[len(payloads)-1])
	}
}

// 4.1-UNIT-020 (P0) — BR-2.6 boundary: when the upstream stream errors
// after the first chunk, the chunker returns the error (the handler then
// emits an SSE error frame). The chunker itself does NOT emit [DONE] on
// error — that responsibility belongs to the handler's mid-stream-failure
// path.
func TestAdapterChunker_PostFlushError_ReturnsErrSkipsDONE(t *testing.T) {
	expected := connect.NewError(connect.CodeUnavailable, errors.New("vendor RST"))
	stream := &fakeAdapterStream{
		chunks: []*adapterv1.ChatChunk{
			chunkDelta("chatcmpl-rst", "assistant", "hello", nil),
		},
		err: expected,
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	chunks, _, err := chunker.Stream(context.Background(), w)
	if !errors.Is(err, expected) {
		t.Fatalf("err = %v, want %v", err, expected)
	}
	if chunks != 1 {
		t.Fatalf("chunksEmitted = %d, want 1", chunks)
	}
	if strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("chunker should NOT emit [DONE] when stream errored; body=%s", rec.Body.String())
	}
}

// 4.1-UNIT-020b — pre-flush failure: Stream errors with no chunks emitted →
// caller can inspect HeadersFlushed()=false and emit JSON envelope (BR-2.5).
func TestAdapterChunker_PreFlushFailure_HeadersNotFlushed(t *testing.T) {
	expected := connect.NewError(connect.CodeUnavailable, errors.New("dial failed"))
	stream := &fakeAdapterStream{
		chunks: nil,
		err:    expected,
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	chunks, _, err := chunker.Stream(context.Background(), w)
	if !errors.Is(err, expected) {
		t.Fatalf("err = %v, want %v", err, expected)
	}
	if chunks != 0 {
		t.Fatalf("chunksEmitted = %d, want 0", chunks)
	}
	if w.HeadersFlushed() {
		t.Fatalf("BR-2.5: HeadersFlushed() = true; expected false (no event emitted)")
	}
}

// 4.1-UNIT-020c — context cancellation between chunks exits cleanly with
// ctx.Err().
func TestAdapterChunker_ContextCancellation(t *testing.T) {
	stream := &fakeAdapterStream{
		chunks: []*adapterv1.ChatChunk{
			chunkDelta("chatcmpl-c", "assistant", "first", nil),
			chunkDelta("chatcmpl-c", "", "second", nil),
		},
	}
	chunker := NewAdapterChunker(stream, "deepseek-v3")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	rec := httptest.NewRecorder()
	w := NewWriter(rec)
	_, _, err := chunker.Stream(ctx, w)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// extractSSEDataPayloads splits an SSE body into the payloads following
// `data: ` (one per event; payloads strip the prefix and trailing \n\n).
func extractSSEDataPayloads(t *testing.T, body string) []string {
	t.Helper()
	var payloads []string
	for _, raw := range strings.Split(body, "\n\n") {
		raw = strings.TrimRight(raw, "\n")
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "data: ") {
			t.Fatalf("event missing data: prefix: %q", raw)
		}
		payloads = append(payloads, strings.TrimPrefix(raw, "data: "))
	}
	return payloads
}
