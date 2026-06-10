// Story 4.1 — AdapterChunker: bridges a gateway-side adapter Connect-RPC
// server-streaming response into the OpenAI-compatible SSE wire shape.
//
// Sister to MockChunker (Story 3.4) — same Stream(ctx, w) (chunksEmitted,
// firstFlushAt, err) signature so chat_completions_stream.go selects one or
// the other purely by adapter-registry resolution outcome (BR-2.1). The
// streaming package owns no knowledge of adapterclient — the chunk stream
// is supplied via the AdapterChunkStream interface so the gateway-internal
// `adapterclient.Stream` satisfies it implicitly without a cross-package
// import.
//
// BR-2.4 invariant: when the upstream's terminal chunk carries `usage`, the
// chunker forwards it on the LAST `data: <json>\n\n` event BEFORE the
// literal `data: [DONE]\n\n` terminator. Non-terminal chunks emit `usage`
// omitted (json omitempty). Per BR-3.7 the SDK's iterator surfaces
// `chunk.usage` populated on the last non-DONE iteration.
package streaming

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// AdapterChunkStream is the minimal iterator surface the chunker consumes.
// Both the production `adapterclient.Stream` (Connect-RPC server-streaming)
// and the unit-test fake satisfy it implicitly. Keeping the surface here
// avoids a cross-package import (streaming → adapterclient) that would
// invert the package layering.
type AdapterChunkStream interface {
	Receive() bool
	Msg() *adapterv1.ChatChunk
	Err() error
}

// AdapterChunker iterates an adapter Connect-RPC server-stream and forwards
// each ChatChunk as a `data: <json>\n\n` SSE event into Writer w.
//
// Story 5.3 ISSUE-001 — the chunker tracks the LAST non-nil `chunk.Usage`
// it saw during the iteration and exposes it via TailUsage(). The streaming
// handler reads this AFTER Stream() returns to decide the post-deduction
// path per Architect Q10:
//
//	(i)  normal completion + tail usage     → TPMDeduct(total_tokens)
//	(ii) stream error + tail usage          → TPMDeduct(prompt_tokens) — partial
//	(iii) client disconnect + tail usage    → TPMDeduct(prompt_tokens) — partial
//	(iv) any path + no tail usage           → NO deduct; slog WARN
type AdapterChunker struct {
	stream    AdapterChunkStream
	model     string
	tailUsage *adapterv1.Usage // captured by Stream(); nil until first usage chunk
	// guard is the optional Story-8.3 §9.3 出参 hook (nil default → byte-identical
	// pre-8.3 loop). lastID/lastCreated are captured as chunks pass so the
	// content_filter terminal echoes the upstream id/created on a mid-stream cut.
	guard       OutputGuard
	lastID      string
	lastCreated int64
}

// NewAdapterChunker builds the chunker. model is the OpenAI-shape `model`
// field forced onto every outbound JSON chunk — Story 4.1 echoes
// req.Model verbatim (failover-aware behaviour lands in Epic 6 routing-svc).
func NewAdapterChunker(stream AdapterChunkStream, model string) *AdapterChunker {
	return &AdapterChunker{stream: stream, model: model}
}

// AttachOutputGuard wires the optional Story-8.3 output guard. A nil guard leaves
// the Stream loop byte-identical to pre-8.3. The handler calls this right after
// construction. Returns the receiver for call-site chaining.
func (c *AdapterChunker) AttachOutputGuard(g OutputGuard) *AdapterChunker {
	c.guard = g
	return c
}

// TailUsage returns the LAST non-nil `chunk.Usage` the chunker saw during
// the most-recent Stream() invocation. Nil if no chunk carried a usage
// field (Architect Q10 case iv — missing-tail-usage). Safe to call after
// Stream() returns, including on error paths (partial captures preserved
// per Q10 case ii/iii). Single-use semantics: subsequent Stream() calls
// reset the field on the first usage chunk encountered.
func (c *AdapterChunker) TailUsage() *adapterv1.Usage {
	return c.tailUsage
}

// Stream consumes the entire adapter chunk stream and emits the OpenAI SSE
// wire sequence. Returns:
//
//	chunksEmitted   number of completed WriteEvent calls (excludes WriteDone)
//	firstFlushAt    wall-clock instant captured just before the first Flush
//	                (zero value if no chunk was emitted; BR-2.7 TTFB anchor)
//	err             ctx.Err() OR stream.Err() OR underlying writer error
//
// On err != nil the handler inspects w.HeadersFlushed() to choose between
// JSON envelope (BR-2.5 pre-flush boundary) and SSE error frame (BR-2.6
// post-flush boundary). The chunker itself does NOT emit [DONE] on error —
// that decision belongs to the handler.
func (c *AdapterChunker) Stream(ctx context.Context, w Writer) (chunksEmitted int, firstFlushAt time.Time, err error) {
	for {
		// Honour cancellation BEFORE the next blocking Receive(). The
		// underlying connect.ServerStreamForClient.Receive will also exit
		// on ctx cancellation, but the explicit check keeps the return
		// path uniform.
		select {
		case <-ctx.Done():
			return chunksEmitted, firstFlushAt, ctx.Err()
		default:
		}
		if !c.stream.Receive() {
			break
		}
		chunk := c.stream.Msg()
		if chunk == nil {
			continue
		}
		// Story 5.3 ISSUE-001 — capture LAST seen usage for the post-
		// deduction decision in serveAdapterStream. Per Story 4.1 BR-2.4
		// usage typically arrives on the terminal chunk; this also
		// captures any interim chunk that carries usage (e.g. an
		// OpenAI `stream_options.include_usage=true`-style early
		// prompt_tokens broadcast) so partial-deduction (Q10 case ii/iii)
		// has a chance of having data.
		if chunk.Usage != nil {
			c.tailUsage = chunk.Usage
		}
		// Capture id/created so a content_filter terminal can echo the upstream
		// envelope on a mid-stream cut (Story 8.3).
		if chunk.Id != "" {
			c.lastID = chunk.Id
		}
		if chunk.Created != 0 {
			c.lastCreated = chunk.Created
		}
		// Story 8.3 — §9.3 出参 guard, scan-BEFORE-forward (OQ-8.3-2). Observe the
		// content delta BEFORE marshaling/forwarding; a hit means this delta
		// COMPLETES a sensitive term → withhold it and terminate with a
		// content_filter terminal. The handler reads the captured Match post-Stream.
		if c.guard != nil {
			if delta := chunkDeltaContent(chunk); delta != "" {
				if _, blocked := c.guard.Observe(delta); blocked {
					return c.writeContentFilterTerminal(w, chunksEmitted, firstFlushAt)
				}
			}
		}
		jsonBytes, jerr := marshalAdapterChunk(chunk, c.model)
		if jerr != nil {
			return chunksEmitted, firstFlushAt, jerr
		}
		if err = w.WriteEvent("", jsonBytes); err != nil {
			return chunksEmitted, firstFlushAt, err
		}
		chunksEmitted++
		if chunksEmitted == 1 {
			firstFlushAt = time.Now()
		}
		if err = w.Flush(); err != nil {
			return chunksEmitted, firstFlushAt, err
		}
	}
	if e := c.stream.Err(); e != nil {
		return chunksEmitted, firstFlushAt, e
	}
	if err = w.WriteDone(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	return chunksEmitted, firstFlushAt, nil
}

// chunkDeltaContent concatenates the content deltas of a proto ChatChunk's
// choices (a chunk normally carries one choice with the incremental content).
// Returns "" when no choice carries content (e.g. a usage-only tail chunk) so the
// guard skips the Observe call (scan only the string content surface, AC scope).
func chunkDeltaContent(chunk *adapterv1.ChatChunk) string {
	var sb strings.Builder
	for _, ch := range chunk.Choices {
		if ch.Delta != nil && ch.Delta.Content != nil {
			sb.WriteString(*ch.Delta.Content)
		}
	}
	return sb.String()
}

// writeContentFilterTerminal emits the Story-8.3 §9.3 出参 termination for the
// adapter path: one terminal chunk with an EMPTY delta (OQ-8.3-4) +
// finish_reason:"content_filter", echoing the captured upstream id/created, then
// [DONE]. Same WriteEvent+WriteDone path normal chunks use (BR-2.6 — NOT
// writeSSEErrorFrame). Returns ErrContentFiltered. When the hit lands on the very
// first delta (no chunk flushed yet) the terminal write flushes headers → a 200
// SSE content_filter response, not a JSON error envelope (AC2 first-delta case).
func (c *AdapterChunker) writeContentFilterTerminal(w Writer, chunksEmitted int, firstFlushAt time.Time) (int, time.Time, error) {
	cf := "content_filter"
	terminal := adapterChunkJSON{
		ID:      c.lastID,
		Object:  "chat.completion.chunk",
		Created: c.lastCreated,
		Model:   c.model,
		Choices: []adapterChunkChoiceJSON{{Index: 0, Delta: adapterChunkDeltaJSON{}, FinishReason: &cf}},
	}
	terminalJSON, _ := json.Marshal(terminal)
	if err := w.WriteEvent("", terminalJSON); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	chunksEmitted++
	if firstFlushAt.IsZero() {
		firstFlushAt = time.Now() // first byte is the terminal (hit on the first delta)
	}
	if err := w.Flush(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	if err := w.WriteDone(); err != nil {
		return chunksEmitted, firstFlushAt, err
	}
	return chunksEmitted, firstFlushAt, ErrContentFiltered
}

// adapterChunkJSON is the OpenAI `chat.completion.chunk` wire shape. Drives
// the JSON output for both content-delta chunks (Usage omitted) and the
// terminal chunk (Usage populated).
type adapterChunkJSON struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []adapterChunkChoiceJSON `json:"choices"`
	Usage   *adapterUsageJSON        `json:"usage,omitempty"`
}

type adapterChunkChoiceJSON struct {
	Index        int                   `json:"index"`
	Delta        adapterChunkDeltaJSON `json:"delta"`
	FinishReason *string               `json:"finish_reason"`
}

type adapterChunkDeltaJSON struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type adapterUsageJSON struct {
	PromptTokens     int32 `json:"prompt_tokens"`
	CompletionTokens int32 `json:"completion_tokens"`
	TotalTokens      int32 `json:"total_tokens"`
}

// marshalAdapterChunk renders the proto ChatChunk into the OpenAI-compatible
// JSON body the SDK's streaming iterator parses. Object is always
// `chat.completion.chunk`; the model field echoes the gateway-level
// dispatch decision (Story 4.1 — req.Model verbatim).
func marshalAdapterChunk(chunk *adapterv1.ChatChunk, model string) ([]byte, error) {
	choices := make([]adapterChunkChoiceJSON, len(chunk.Choices))
	for i, c := range chunk.Choices {
		choice := adapterChunkChoiceJSON{
			Index:        int(c.Index),
			FinishReason: c.FinishReason,
		}
		if c.Delta != nil {
			if c.Delta.Role != nil {
				choice.Delta.Role = *c.Delta.Role
			}
			if c.Delta.Content != nil {
				choice.Delta.Content = *c.Delta.Content
			}
		}
		choices[i] = choice
	}
	out := adapterChunkJSON{
		ID:      chunk.Id,
		Object:  "chat.completion.chunk",
		Created: chunk.Created,
		Model:   model,
		Choices: choices,
	}
	if chunk.Usage != nil {
		out.Usage = &adapterUsageJSON{
			PromptTokens:     chunk.Usage.GetPromptTokens(),
			CompletionTokens: chunk.Usage.GetCompletionTokens(),
			TotalTokens:      chunk.Usage.GetTotalTokens(),
		}
	}
	return json.Marshal(out)
}
