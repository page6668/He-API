// Story 3.4 — Mock chunker that splits a content string into word-boundary
// chunks and emits the OpenAI `chat.completion.chunk` SSE event sequence.
//
// Emission order:
//  1. bootstrap chunk: delta:{role:"assistant"}, finish_reason:null
//  2. N content chunks: delta:{content:<piece>}, finish_reason:null
//  3. terminal chunk:   delta:{},               finish_reason:"stop"
//  4. literal `data: [DONE]\n\n` sentinel via Writer.WriteDone() (BR-4.5)
//
// Per Architect Round 1 AR1-R4, the chunker — not the Writer — owns clock
// capture: Stream(...) returns firstFlushAt time.Time captured immediately
// before the first Flush(). The handler reads firstFlushAt off the return
// value to compute ttfb_ms.
//
// Per Architect Round 1 C1 fix: NewMockChunker takes PRIMITIVES (model,
// content as strings) — the streaming package MUST NOT import handlers.
// The handler-side call site supplies handlers.MockContent + req.Model.
package streaming

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// chatChunk is the OpenAI `chat.completion.chunk` object. JSON tags
// snake_case per coding-standards.md §12.3.
type chatChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
}

// chatChunkChoice mirrors OpenAI's streaming choice. FinishReason is
// *string so it emits JSON null on in-flight chunks and "stop" on the
// terminal chunk (BR-1.3).
type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// chatChunkDelta carries the per-chunk incremental update. CRITICAL: both
// Role and Content carry `omitempty` so the three BR-1.3 shapes serialize
// correctly (bootstrap → {"role":"assistant"}; content → {"content":"..."};
// terminal → {}). Architect Round 1 AR1-R2: the {} shape is OpenAI's
// canonical emission — removing omitempty would emit {"role":"","content":""}
// which is non-spec.
type chatChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// MockChunker emits the Story-3.4 mock SSE stream. One per request — the
// struct holds per-request state (id + created) that must be threaded
// through every chunk per BR-1.4 / BR-1.5.
type MockChunker struct {
	model   string
	content string
	id      string
	created int64
	// guard is the optional Story-8.3 §9.3 出参 hook (nil default → byte-identical
	// pre-8.3 loop). When set, each content piece is Observe-d BEFORE it is
	// forwarded; a hit terminates the stream with a content_filter terminal.
	guard OutputGuard
}

// AttachOutputGuard wires the optional Story-8.3 output guard. A nil guard leaves
// the Stream loop byte-identical to pre-8.3 (zero overhead / no observable
// change). The handler calls this right after construction. Returns the receiver
// for call-site chaining.
func (c *MockChunker) AttachOutputGuard(g OutputGuard) *MockChunker {
	c.guard = g
	return c
}

// NewMockChunker builds a chunker. All arguments are primitives — the
// streaming package does NOT import handlers (AR1-C1 fix). The handler
// supplies handlers.MockContent and req.Model at the call site.
func NewMockChunker(model, content, id string, created int64) *MockChunker {
	return &MockChunker{model: model, content: content, id: id, created: created}
}

// Stream emits the full SSE event sequence to w. Returns:
//   - chunksEmitted: count of completed WriteEvent calls (excludes WriteDone)
//   - firstFlushAt: wall-clock instant captured IMMEDIATELY BEFORE the first
//     Flush() invocation (AR1-R4 — TTFB observation point). Zero value if
//     the context is cancelled BEFORE the chunker reaches its first Flush.
//   - err: any error from writer.WriteEvent / writer.Flush / writer.WriteDone
//
// Context cancellation between chunks exits the loop cleanly without writing
// a partial chunk (BR-4.1) — the select check is BEFORE each WriteEvent.
func (c *MockChunker) Stream(ctx context.Context, w Writer) (chunksEmitted int, firstFlushAt time.Time, err error) {
	// Bootstrap chunk — delta:{role:"assistant"}, finish_reason:null.
	bootstrap := c.newChunk(chatChunkDelta{Role: "assistant"}, nil)
	bootstrapJSON, _ := json.Marshal(bootstrap)
	if err = w.WriteEvent("", bootstrapJSON); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	chunksEmitted++

	// First flush — capture the TTFB instant IMMEDIATELY before invoking
	// Flush so the handler can compute ttfb_ms from the returned value.
	firstFlushAt = time.Now()
	if err = w.Flush(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}

	// Content chunks — word-boundary split at unicode.IsSpace boundaries.
	pieces := splitContentIntoChunks(c.content, 8)
	for _, piece := range pieces {
		select {
		case <-ctx.Done():
			return chunksEmitted, firstFlushAt, ctx.Err()
		default:
		}
		// Story 8.3 — §9.3 出参 guard, scan-BEFORE-forward (OQ-8.3-2). A hit means
		// this piece COMPLETES a sensitive term: withhold it (do NOT WriteEvent) and
		// terminate with a content_filter terminal. The captured Match is read by the
		// handler from the guard post-Stream (M-1: chunker writes the terminal, handler
		// records + meters).
		if c.guard != nil {
			if _, blocked := c.guard.Observe(piece); blocked {
				return c.writeContentFilterTerminal(w, chunksEmitted, firstFlushAt)
			}
		}
		ck := c.newChunk(chatChunkDelta{Content: piece}, nil)
		j, _ := json.Marshal(ck)
		if err = w.WriteEvent("", j); err != nil {
			return chunksEmitted, firstFlushAt, err
		}
		chunksEmitted++
		if err = w.Flush(); err != nil {
			return chunksEmitted, firstFlushAt, err
		}
	}

	// Cancellation check before terminal chunk — keeps the early-exit
	// semantics consistent across the full loop.
	select {
	case <-ctx.Done():
		return chunksEmitted, firstFlushAt, ctx.Err()
	default:
	}

	// Terminal chunk — delta:{}, finish_reason:"stop".
	stop := "stop"
	terminal := c.newChunk(chatChunkDelta{}, &stop)
	terminalJSON, _ := json.Marshal(terminal)
	if err = w.WriteEvent("", terminalJSON); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	chunksEmitted++
	if err = w.Flush(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}

	// [DONE] sentinel.
	if err = w.WriteDone(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	return chunksEmitted, firstFlushAt, nil
}

// writeContentFilterTerminal emits the Story-8.3 §9.3 出参 termination: a single
// terminal chunk with an EMPTY delta (OQ-8.3-4 — no notice string in a stream
// terminal; the client has already appended prior deltas, so the finish_reason IS
// the contract) + finish_reason:"content_filter", then the literal [DONE]. It uses
// the SAME WriteEvent+WriteDone path the normal "stop" terminal uses (BR-2.6 —
// NOT writeSSEErrorFrame, which is for upstream errors). Returns ErrContentFiltered
// so the handler records the 8.5 event + meters the consumed tail (M-1).
func (c *MockChunker) writeContentFilterTerminal(w Writer, chunksEmitted int, firstFlushAt time.Time) (int, time.Time, error) {
	cf := "content_filter"
	terminal := c.newChunk(chatChunkDelta{}, &cf) // empty delta → {} (omitempty), finish_reason set
	terminalJSON, _ := json.Marshal(terminal)
	if err := w.WriteEvent("", terminalJSON); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	chunksEmitted++
	if err := w.Flush(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	if err := w.WriteDone(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	return chunksEmitted, firstFlushAt, ErrContentFiltered
}

func (c *MockChunker) newChunk(delta chatChunkDelta, finish *string) chatChunk {
	return chatChunk{
		ID:      c.id,
		Object:  "chat.completion.chunk",
		Created: c.created,
		Model:   c.model,
		Choices: []chatChunkChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
}

// splitContentIntoChunks splits s into approximately targetN word-boundary
// chunks. Each chunk contains one or more whole words separated by single
// spaces; concatenating all chunks reassembles the original word stream
// (consecutive whitespace runs collapse to a single space — the splitter
// uses unicode.IsSpace boundary semantics per BR-4.3 / AR1-R1).
//
// Edge cases (covered by 3.4-UNIT-018):
//   - empty / whitespace-only input → empty slice
//   - targetN <= 0 → empty slice
//   - len(words) < targetN → one chunk per word
//   - canonical MockContent (10 words) + targetN=8 → 8 chunks
func splitContentIntoChunks(s string, targetN int) []string {
	if targetN <= 0 {
		return nil
	}
	words := strings.FieldsFunc(s, unicode.IsSpace)
	if len(words) == 0 {
		return nil
	}
	if len(words) <= targetN {
		// One chunk per word.
		out := make([]string, len(words))
		for i, w := range words {
			if i < len(words)-1 {
				out[i] = w + " "
			} else {
				out[i] = w
			}
		}
		return out
	}
	// Distribute words into targetN chunks as evenly as possible. The first
	// `extra` chunks get one extra word (ceil-distribution).
	base := len(words) / targetN
	extra := len(words) % targetN
	out := make([]string, 0, targetN)
	pos := 0
	for i := 0; i < targetN; i++ {
		take := base
		if i < extra {
			take++
		}
		piece := strings.Join(words[pos:pos+take], " ")
		pos += take
		if i < targetN-1 {
			piece += " "
		}
		out = append(out, piece)
	}
	return out
}
