// Package contentsafety is the in-gateway consumer of the Story-8.1
// safety-lexicon: it segments inbound request text and resolves it against the
// single source-of-truth lexicon, realizing the §9.3 入参 Filter pipeline
// layers (1) Bloom 快速排除 + (2) 词典匹配 for Story 8.2.
//
// It lives in apps/api-gateway/internal/ (NOT a top-level package) because the
// consumer IS the gateway — the billinggate placement precedent (OQ-8.2-7,
// Architect-ratified). 8.1 deliberately deferred segmentation to the consumer
// (8.1 BR-3.4); this package owns it. It depends ONLY on packages/safety-lexicon
// (never on the handlers package — the handler calls ScanText per message, which
// keeps this package import-cycle-free).
package contentsafety

import "context"

// SafetyEvent is the structured interception record handed to a Recorder when an
// inbound request is blocked. Its field shapes EXACTLY match the Story-8.5
// content_safety_logs columns (direction VARCHAR(10) / matched_rule VARCHAR(100)
// / action VARCHAR(20), per docs/architecture/data-models.md:142-154) so Story
// 8.5 binds its persisting Recorder with no contract change (BR-3.2).
//
// Story 8.2 deliberately does NOT populate an excerpt/redacted snippet — placing
// raw user text in the event is Story 8.5's job (with its redaction policy). The
// matched term reaches only this event, never the caller-facing error envelope
// (no lexicon leak, AC1 security note).
type SafetyEvent struct {
	Direction   string // "input" (入参, Story 8.2) | "output" (出参, Story 8.3) — fits direction VARCHAR(10)
	MatchedRule string // == safetylexicon.Match.Canonical (≤100 runes, 8.1 cap)
	Category    string // safetylexicon.Match.Category (§9.3 taxonomy)
	Severity    string // safetylexicon.Match.Severity (carried; NOT gating in 8.2 — 8.4 owns strictness)
	Action      string // literal "blocked"
	UserID      string // bearer-auth owner user_id (middleware context)
	APIKeyID    string // validated api_keys.id (middleware context)
	HeRequestID string // request correlation id (requestid context)
}

// Direction / Action literals — the only values Story 8.2 ever emits. Exported so
// the handler and Story 8.5 share one constant rather than re-typing the string.
const (
	DirectionInput = "input"
	// DirectionOutput is the Story-8.3 §9.3 出参 direction. Additive const (the
	// SafetyEvent struct, Recorder interface, and NopRecorder default are REUSED
	// verbatim from 8.2). Fits content_safety_logs.direction VARCHAR(10); Story
	// 8.5 binds ONE persisting Recorder for BOTH directions with no contract change.
	DirectionOutput = "output"
	ActionBlocked   = "blocked"
)

// Recorder is the Story-8.5 binding seam. A confirmed input hit constructs a
// SafetyEvent and hands it to the injected Recorder. The reject-path contract is
// fire-and-forget: Record returns nothing and MUST NOT block the 400 response;
// Story 8.5's persisting implementation owns its own failure/retry policy.
type Recorder interface {
	Record(ctx context.Context, ev SafetyEvent)
}

// NopRecorder is the Story-8.2 default Recorder: it persists nothing. Story 8.2
// makes ZERO database changes and writes NO content_safety_logs row — the
// persisting implementation is supplied by Story 8.5. Wiring a no-op default
// (rather than a nil interface) keeps the handler's call site branch-free.
type NopRecorder struct{}

// Record discards the event (no DB write, no I/O, no panic).
func (NopRecorder) Record(context.Context, SafetyEvent) {}
