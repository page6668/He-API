// Story 3.4 — MockChunker + splitter tests (chat_chunks.go).
//
// Test Design: docs/qa/assessments/3.4-test-design-20260518.md
// Architect Round 1 Rulings honored: AR1-R1 (word-boundary), AR1-R2 ({} via omitempty),
// AR1-R3 (dedicated WriteDone), AR1-R4 (return-value firstFlushAt — no Writer hook)

package streaming

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// mockContentForTests mirrors handlers.MockContent verbatim. The streaming
// package MUST NOT import handlers per Architect Round 1 C1 fix; this copy
// exists only for test inputs + the concatenation assertion (UNIT-017,
// BLIND-BOUNDARY-002). Single-source-of-truth lives in handlers.MockContent.
const mockContentForTests = "Hello from He-API mock. Real upstream lands in Story 4.x."

// ----- writer stub: records every call in order ----------------------------

type recordedCall struct {
	method string // "WriteEvent" | "WriteDone" | "Flush" | "Close"
	event  string
	data   []byte
	at     time.Time
}

type stubWriter struct {
	mu          sync.Mutex
	calls       []recordedCall
	flushErr    error
	writeErr    error
	writeDoneOK bool
}

func newStubWriter() *stubWriter { return &stubWriter{writeDoneOK: true} }

func (s *stubWriter) WriteEvent(event string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	s.calls = append(s.calls, recordedCall{method: "WriteEvent", event: event, data: cp, at: time.Now()})
	return s.writeErr
}
func (s *stubWriter) WriteDone() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{method: "WriteDone", at: time.Now()})
	if !s.writeDoneOK {
		return errFromTest("WriteDone failed")
	}
	return nil
}
func (s *stubWriter) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{method: "Flush", at: time.Now()})
	return s.flushErr
}
func (s *stubWriter) Close() error { return nil }

func (s *stubWriter) writeEvents() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedCall, 0, len(s.calls))
	for _, c := range s.calls {
		if c.method == "WriteEvent" {
			out = append(out, c)
		}
	}
	return out
}

func (s *stubWriter) snapshot() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedCall, len(s.calls))
	copy(out, s.calls)
	return out
}

type errFromTest string

func (e errFromTest) Error() string { return string(e) }

func decodeChunk(t *testing.T, data []byte) chatChunk {
	t.Helper()
	var c chatChunk
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("decode chunk: %v\nbytes=%s", err, string(data))
	}
	return c
}

// ============================================================
// AC1.B — MockChunker Stream Shape + Ordering (Unit)
// ============================================================

// Scenario: 3.4-UNIT-009 — Priority P0 — Level unit
// BR-1.3: bootstrap chunk emitted FIRST with delta:{role:"assistant"} + finish_reason:null
func TestMockChunker_Stream_EmitsBootstrapRoleChunkFirst(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-bootstrap0001", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := sw.writeEvents()
	if len(events) == 0 {
		t.Fatal("no WriteEvent calls recorded")
	}
	first := decodeChunk(t, events[0].data)
	if first.Choices[0].Delta.Role != "assistant" {
		t.Errorf("bootstrap delta.role = %q, want \"assistant\"", first.Choices[0].Delta.Role)
	}
	if first.Choices[0].Delta.Content != "" {
		t.Errorf("bootstrap delta.content = %q, want \"\"", first.Choices[0].Delta.Content)
	}
	if first.Choices[0].FinishReason != nil {
		t.Errorf("bootstrap finish_reason = %v, want nil", *first.Choices[0].FinishReason)
	}
	if !strings.Contains(string(events[0].data), `"delta":{"role":"assistant"}`) {
		t.Errorf("bootstrap delta byte-shape mismatch: %s", string(events[0].data))
	}
}

// Scenario: 3.4-UNIT-010 — Priority P0 — Level unit
// BR-1.3: content chunks have delta.content non-empty + finish_reason:null
func TestMockChunker_Stream_EmitsContentChunksWithDeltaContent(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-content0002", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := sw.writeEvents()
	if len(events) < 3 {
		t.Fatalf("need at least 3 events (bootstrap + content + terminal); got %d", len(events))
	}
	for i := 1; i < len(events)-1; i++ {
		ck := decodeChunk(t, events[i].data)
		if ck.Choices[0].Delta.Content == "" {
			t.Errorf("event %d: delta.content empty (expected non-empty)", i)
		}
		if ck.Choices[0].Delta.Role != "" {
			t.Errorf("event %d: delta.role = %q (expected empty)", i, ck.Choices[0].Delta.Role)
		}
		if ck.Choices[0].FinishReason != nil {
			t.Errorf("event %d: finish_reason = %v (expected nil)", i, *ck.Choices[0].FinishReason)
		}
	}
}

// Scenario: 3.4-UNIT-011 — Priority P0 — Level unit
// BR-1.3 + AR1-R2: terminal chunk has delta:{} (via omitempty) + finish_reason:"stop"
func TestMockChunker_Stream_EmitsTerminalStopChunk(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-terminal003", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := sw.writeEvents()
	terminal := events[len(events)-1]
	if !strings.Contains(string(terminal.data), `"delta":{}`) {
		t.Errorf("terminal delta byte-shape mismatch (want delta:{}): %s", string(terminal.data))
	}
	ck := decodeChunk(t, terminal.data)
	if ck.Choices[0].FinishReason == nil || *ck.Choices[0].FinishReason != "stop" {
		t.Errorf("terminal finish_reason = %v, want \"stop\"", ck.Choices[0].FinishReason)
	}
	if ck.Choices[0].Delta.Role != "" || ck.Choices[0].Delta.Content != "" {
		t.Errorf("terminal delta should be empty struct; got role=%q content=%q",
			ck.Choices[0].Delta.Role, ck.Choices[0].Delta.Content)
	}
}

// Scenario: 3.4-UNIT-012 — Priority P0 — Level unit
// BR-4.5 + AR1-R3: last writer call is WriteDone() (NOT WriteEvent)
func TestMockChunker_Stream_EmitsDoneSentinelLast(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-done0000000004", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	snap := sw.snapshot()
	if len(snap) == 0 {
		t.Fatal("no calls recorded")
	}
	last := snap[len(snap)-1]
	if last.method != "WriteDone" {
		t.Errorf("last call = %s, want WriteDone", last.method)
	}
}

// Scenario: 3.4-UNIT-013 — Priority P0 — Level unit
// BR-1.4: id threaded through ALL chunks
func TestMockChunker_Stream_AllChunksShareSameID(t *testing.T) {
	const id = "chatcmpl-mock-abc123def456"
	c := NewMockChunker("qwen-max", mockContentForTests, id, 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for i, e := range sw.writeEvents() {
		ck := decodeChunk(t, e.data)
		if ck.ID != id {
			t.Errorf("event %d: id = %q, want %q", i, ck.ID, id)
		}
	}
}

// Scenario: 3.4-UNIT-014 — Priority P0 — Level unit
// BR-1.5: created threaded through ALL chunks
func TestMockChunker_Stream_AllChunksShareSameCreated(t *testing.T) {
	const created = int64(1715000000)
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-created00005", created)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for i, e := range sw.writeEvents() {
		ck := decodeChunk(t, e.data)
		if ck.Created != created {
			t.Errorf("event %d: created = %d, want %d", i, ck.Created, created)
		}
	}
}

// Scenario: 3.4-UNIT-015 — Priority P0 — Level unit
// BR-1.6: model echoed verbatim — NO remapping
func TestMockChunker_Stream_AllChunksShareModelEcho(t *testing.T) {
	const model = "qwen-evil-payload-but-allowed"
	c := NewMockChunker(model, mockContentForTests, "chatcmpl-mock-modelecho0006", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for i, e := range sw.writeEvents() {
		ck := decodeChunk(t, e.data)
		if ck.Model != model {
			t.Errorf("event %d: model = %q, want %q", i, ck.Model, model)
		}
	}
}

// Scenario: 3.4-UNIT-016 — Priority P0 — Level unit
// BR-1.7: object literal is "chat.completion.chunk"
func TestMockChunker_Stream_AllChunksObjectIsChatCompletionChunk(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-object0000007", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for i, e := range sw.writeEvents() {
		ck := decodeChunk(t, e.data)
		if ck.Object != "chat.completion.chunk" {
			t.Errorf("event %d: object = %q, want \"chat.completion.chunk\"", i, ck.Object)
		}
	}
}

// Scenario: 3.4-UNIT-017 — Priority P0 — Level unit
// BR-3.3: concat of non-bootstrap non-terminal delta.content EQUALS MockContent
func TestMockChunker_Stream_ContentConcatEqualsMockContent(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-concat0000008", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := sw.writeEvents()
	var got string
	for i := 1; i < len(events)-1; i++ {
		ck := decodeChunk(t, events[i].data)
		got += ck.Choices[0].Delta.Content
	}
	if got != mockContentForTests {
		t.Errorf("concat mismatch\n got=%q\nwant=%q", got, mockContentForTests)
	}
}

// ============================================================
// AC1.C — Word-Boundary Splitter (Unit)
// ============================================================

// Scenario: 3.4-UNIT-018 — Priority P0 — Level unit
// BR-4.3 + AR1-R1: table-driven splitContentIntoChunks word-boundary split
func TestSplitContentIntoChunks_WordBoundary(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		targetN int
		want    []string
	}{
		{
			name:    "canonical MockContent + targetN=8",
			input:   mockContentForTests,
			targetN: 8,
			// 10 words; ceil-distribute → first 2 chunks have 2 words, remaining 6 have 1.
			want: []string{
				"Hello from ",
				"He-API mock. ",
				"Real ",
				"upstream ",
				"lands ",
				"in ",
				"Story ",
				"4.x.",
			},
		},
		{
			name:    "empty string",
			input:   "",
			targetN: 8,
			want:    nil,
		},
		{
			name:    "single word",
			input:   "Hello",
			targetN: 8,
			want:    []string{"Hello"},
		},
		{
			name:    "leading/trailing whitespace trimmed",
			input:   "  Hello world  ",
			targetN: 2,
			want:    []string{"Hello ", "world"},
		},
		{
			name:    "multiple consecutive spaces collapse",
			input:   "Hello     world",
			targetN: 2,
			want:    []string{"Hello ", "world"},
		},
		{
			name:    "targetN larger than word count → 1 chunk per word",
			input:   "a b c",
			targetN: 10,
			want:    []string{"a ", "b ", "c"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitContentIntoChunks(tc.input, tc.targetN)
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch\n got (%d) %#v\nwant (%d) %#v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("chunk[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// Scenario: 3.4-UNIT-019 — Priority P0 — Level unit
// BR-4.3 forward-defence: no partial words in canonical split
func TestSplitContentIntoChunks_NoPartialWords(t *testing.T) {
	const targetN = 8
	chunks := splitContentIntoChunks(mockContentForTests, targetN)
	originalWords := strings.Fields(mockContentForTests)
	seenIdx := 0
	for ci, chunk := range chunks {
		for _, w := range strings.Fields(chunk) {
			if seenIdx >= len(originalWords) {
				t.Fatalf("chunk %d emitted extra word %q beyond original stream", ci, w)
			}
			if w != originalWords[seenIdx] {
				t.Errorf("chunk %d token %q does not match original word %q at index %d",
					ci, w, originalWords[seenIdx], seenIdx)
			}
			seenIdx++
		}
	}
	if seenIdx != len(originalWords) {
		t.Errorf("only %d/%d words emitted across all chunks", seenIdx, len(originalWords))
	}
	// Tightness: 10 words / 8 chunks → no chunk holds more than 2 words.
	for ci, chunk := range chunks {
		nWords := len(strings.Fields(chunk))
		if nWords < 1 || nWords > 2 {
			t.Errorf("chunk %d has %d words (expected 1-2 for canonical input)", ci, nWords)
		}
	}
}

// ============================================================
// AC1.D — Cancellation Guard at Chunker Level (Unit)
// ============================================================

// Scenario: 3.4-UNIT-020 — Priority P0 — Level unit
// BR-4.1: <-ctx.Done() check is BEFORE write, NOT after
func TestMockChunker_Stream_HonoursContextCancellation(t *testing.T) {
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-cancel00000020", 1715000000)
	sw := newStubWriter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := c.Stream(ctx, sw)
	if err == nil {
		t.Fatal("Stream returned nil err; expected ctx.Err() after cancellation")
	}
	if !isCanceled(err) {
		t.Errorf("Stream err = %v; expected context.Canceled", err)
	}
	events := sw.writeEvents()
	if len(events) < 1 {
		t.Fatalf("expected >=1 event (bootstrap); got %d", len(events))
	}
	if len(events) > 2 {
		t.Errorf("expected at most 2 events on immediate cancel; got %d", len(events))
	}
	last := events[len(events)-1]
	ck := decodeChunk(t, last.data)
	if ck.Choices[0].FinishReason != nil && *ck.Choices[0].FinishReason == "stop" {
		t.Error("terminal stop chunk leaked through cancellation guard")
	}
	for _, call := range sw.snapshot() {
		if call.method == "WriteDone" {
			t.Error("WriteDone called despite cancellation")
		}
	}
}

func isCanceled(err error) bool {
	for err != nil {
		if err == context.Canceled {
			return true
		}
		type unwrap interface{ Unwrap() error }
		u, ok := err.(unwrap)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Scenario: 3.4-UNIT-021 — Priority P0 — Level unit
// BR-2.2 + BR-4.2: chunker runs in caller goroutine; NO spawn
func TestMockChunker_Stream_NoGoroutineSpawn(t *testing.T) {
	defer goleak.VerifyNone(t)
	c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-noleak00000021", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
}

// Scenario: 3.4-UNIT-022 — Priority P1 — Level unit
// AR1-R4: Stream returns (chunksEmitted, firstFlushAt, err) with ordering invariant
func TestMockChunker_Stream_ReportsChunksEmittedAndFirstFlushAt(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-tt0000000022a", 1715000000)
		sw := newStubWriter()
		chunksEmitted, firstFlushAt, err := c.Stream(context.Background(), sw)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		events := sw.writeEvents()
		if chunksEmitted != len(events) {
			t.Errorf("chunksEmitted = %d, want %d (WriteEvent count)", chunksEmitted, len(events))
		}
		if firstFlushAt.IsZero() {
			t.Fatal("firstFlushAt is zero; expected non-zero on happy path")
		}
		snap := sw.snapshot()
		var bootstrapAt, firstContentAt, firstFlushCallAt time.Time
		eventCount := 0
		for _, call := range snap {
			switch call.method {
			case "WriteEvent":
				eventCount++
				if eventCount == 1 {
					bootstrapAt = call.at
				} else if eventCount == 2 && firstContentAt.IsZero() {
					firstContentAt = call.at
				}
			case "Flush":
				if firstFlushCallAt.IsZero() {
					firstFlushCallAt = call.at
				}
			}
		}
		if firstFlushAt.Before(bootstrapAt) {
			t.Errorf("firstFlushAt (%v) is BEFORE bootstrap WriteEvent (%v) — ordering violation",
				firstFlushAt, bootstrapAt)
		}
		if !firstContentAt.IsZero() && firstFlushAt.After(firstContentAt) {
			t.Errorf("firstFlushAt (%v) is AFTER first content WriteEvent (%v) — ordering violation",
				firstFlushAt, firstContentAt)
		}
		if !firstFlushCallAt.IsZero() && firstFlushAt.After(firstFlushCallAt) {
			t.Errorf("firstFlushAt (%v) is AFTER recorded Flush() return (%v) — capture too late",
				firstFlushAt, firstFlushCallAt)
		}
	})
	t.Run("cancel before first flush → zero firstFlushAt", func(t *testing.T) {
		c := NewMockChunker("qwen-max", mockContentForTests, "chatcmpl-mock-tt0000000022b", 1715000000)
		sw := newStubWriter()
		// Inject a WriteEvent failure to force the chunker to fail BEFORE
		// the firstFlushAt capture line — proves the zero-value sentinel.
		sw.writeErr = errFromTest("boom")
		_, firstFlushAt, err := c.Stream(context.Background(), sw)
		if err == nil {
			t.Fatal("expected error from WriteEvent failure")
		}
		if !firstFlushAt.IsZero() {
			t.Errorf("firstFlushAt = %v; expected zero value when chunker fails before first Flush",
				firstFlushAt)
		}
	})
}

// ============================================================
// Blind-Spot Overlay — BOUNDARY scenarios
// ============================================================

// Scenario: 3.4-BLIND-BOUNDARY-001 — Priority P1 — Level unit
// [BLIND-SPOT BOUNDARY-001] empty/null input
func TestMockChunker_Stream_EmptyContent_NoContentChunks(t *testing.T) {
	c := NewMockChunker("qwen-max", "", "chatcmpl-mock-empty00000bd1", 1715000000)
	sw := newStubWriter()
	chunksEmitted, firstFlushAt, err := c.Stream(context.Background(), sw)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if chunksEmitted != 2 {
		t.Errorf("chunksEmitted = %d, want 2 (bootstrap + terminal)", chunksEmitted)
	}
	if firstFlushAt.IsZero() {
		t.Errorf("firstFlushAt is zero; bootstrap WAS flushed so it must be non-zero")
	}
	events := sw.writeEvents()
	if len(events) != 2 {
		t.Fatalf("want 2 WriteEvent calls; got %d", len(events))
	}
	first := decodeChunk(t, events[0].data)
	if first.Choices[0].Delta.Role != "assistant" {
		t.Errorf("first event is not bootstrap; delta.role=%q", first.Choices[0].Delta.Role)
	}
	last := decodeChunk(t, events[1].data)
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Errorf("last event is not terminal stop; finish_reason=%v", last.Choices[0].FinishReason)
	}
}

// Scenario: 3.4-BLIND-BOUNDARY-002 — Priority P1 — Level unit
// [BLIND-SPOT BOUNDARY-002] minimum value
func TestMockChunker_Stream_SingleWordContent_OneContentChunk(t *testing.T) {
	c := NewMockChunker("qwen-max", "Hello", "chatcmpl-mock-single00000bd2", 1715000000)
	sw := newStubWriter()
	if _, _, err := c.Stream(context.Background(), sw); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := sw.writeEvents()
	if len(events) != 3 {
		t.Fatalf("want 3 events (bootstrap + 1 content + terminal); got %d", len(events))
	}
	content := decodeChunk(t, events[1].data)
	if content.Choices[0].Delta.Content != "Hello" {
		t.Errorf("content chunk delta.content = %q, want \"Hello\"", content.Choices[0].Delta.Content)
	}
}
