// Package analyticslog is the gateway-side `request.logged` PRODUCER (Story 9.1
// AC1). On EVERY terminal /v1/chat/completions + /v1/embeddings outcome —
// success AND every error envelope — the Middleware emits ONE UsageLogEvent
// fire-and-forget (BR-ING-4: a producer error NEVER affects, delays, or mutates
// the already-served response). The 成功率 metric needs the FAILED rows that the
// success-only billing `usage.recorded` event structurally cannot carry (Q-EVT),
// so this is a NEW topic with status/latency/cost on every outcome.
//
// PII discipline (BR-ING-5): the event carries NO message content, NO completion
// text, NO plaintext key. cost_usd is gateway-emitted, NON-NULL, string-decimal
// (Q-COST / H-1) — "" defaults to "0" at emit.
package analyticslog

import "context"

// Record is the mutable per-request analytics accumulator. The handler enriches
// it where it already computes the served facts (the emitUsage seam); the
// Middleware reads it on terminal response and emits ONE event. Fields the
// handler does not set fall back to safe zero values (tokens=0 on an error
// row — the 成功率 denominator row that usage.recorded misses).
type Record struct {
	Model            string // served model (== upstream for the single-leg path)
	PromptTokens     uint32
	CompletionTokens uint32
	TotalTokens      uint32
	CostUsd          string // "" → "0" at emit (NON-NULL, Q-COST H-1)
	TtfbMs           uint32 // streaming time-to-first-byte
	IsStreaming      bool

	// set guards the A/B path: Q-AB mandates ONE event with the served-leg
	// attribution (leg-A), so the FIRST emitUsage call wins and later legs are
	// ignored for request.logged (billing still emits one usage.recorded per leg).
	set bool
}

// Populate fills the served facts ONCE (first call wins — Q-AB leg-A). Safe to
// call on a nil receiver (handlers built without the analytics middleware in
// focused unit tests carry no Record in context).
func (r *Record) Populate(model string, promptTokens, completionTokens, totalTokens uint32, streaming bool) {
	if r == nil || r.set {
		return
	}
	r.set = true
	r.Model = model
	r.PromptTokens = promptTokens
	r.CompletionTokens = completionTokens
	r.TotalTokens = totalTokens
	r.IsStreaming = streaming
}

type ctxKey struct{}

// NewContext returns a child context carrying a fresh Record plus the Record
// pointer the handler enriches.
func NewContext(ctx context.Context) (context.Context, *Record) {
	r := &Record{}
	return context.WithValue(ctx, ctxKey{}, r), r
}

// FromContext returns the Record attached by the Middleware, or nil when the
// request did not pass through it (focused unit tests). Callers MUST tolerate a
// nil Record (Record.Populate is nil-safe).
func FromContext(ctx context.Context) *Record {
	r, _ := ctx.Value(ctxKey{}).(*Record)
	return r
}
