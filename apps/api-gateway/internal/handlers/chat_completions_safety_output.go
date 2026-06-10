// Story 8.3 — §9.3 出参 Filter (OUTPUT direction). The bidirectional counterpart
// of the 8.2 入参 reject: where 8.2 rejects a sensitive REQUEST before dispatch
// (400_content_filter error), 8.3 filters a sensitive model-generated RESPONSE
// AFTER dispatch. A filtered output is a request that SUCCEEDED (the upstream
// produced tokens) and only the generated content was filtered, so the signal is
// the OpenAI-native success-body finish_reason:"content_filter" (status 200) —
// NOT the §5.1.2 error envelope (the 400_content_filter code stays INPUT-only).
//
// This file owns the NON-STREAM path: scan the assembled completion + redact in
// place at the 3 write sites (mock, adapter non-stream, A/B merge). The STREAM
// path (StreamGuard + chunker seam) lives in chat_completions_stream.go. Both
// reuse the SAME 8.2 contentsafety.Scanner core + the Recorder/SafetyEvent seam
// (direction:"output").
package handlers

import (
	"context"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// redactedOutputNotice is the fixed, bilingual, non-leaking replacement for a
// filtered NON-STREAM choice (OQ-8.3-5, Architect-APPROVED). It carries NO
// matched substring (no lexicon leak) and is deterministic + 备案-defensible.
// The STREAM terminal deliberately carries EMPTY content instead (OQ-8.3-4): a
// stream client has already appended prior deltas, so a notice would concatenate
// onto leaked partial text and garble the output — the finish_reason IS the
// contract there.
const redactedOutputNotice = "[内容已被内容安全策略过滤 / Content removed by the content safety policy]"

// WithOutputSafetyScanner wires the Story-8.3 §9.3 出参 content-safety scanner.
// When set, every model-generated completion is scanned against the 8.1 lexicon
// before the 200 body is written (non-stream) / as it streams (stream); a
// confirmed hit redacts (non-stream) or terminates (stream) with
// finish_reason:"content_filter". Nil is silently ignored — a handler built
// without this option does NOT filter output (pre-8.3 behaviour, byte-identical).
// Production wires a non-nil DefaultLexicon-backed scanner (MAY be the SAME
// instance as WithSafetyScanner, OQ-8.3-4).
func WithOutputSafetyScanner(s *contentsafety.Scanner) ChatHandlerOption {
	return func(h *ChatCompletionsHandler) {
		if s != nil {
			h.outputScanner = s
		}
	}
}

// redactResponse scans every choice's message content via the REUSED output
// scanner and, on the FIRST confirmed hit in a choice, REPLACES that choice's
// content with the non-leaking 脱敏 notice + sets finish_reason="content_filter"
// (AC1). Each redacted choice is scanned + redacted INDEPENDENTLY (A/B returns
// ≥2 choices; per-choice x_he_model attribution is preserved), and records ONE
// Story-8.5 output event PER REDACTED CHOICE (OQ-8.3-7 / BR-3.3).
//
// It mutates resp in place and NEVER touches resp.Usage — redaction is body-only;
// the upstream WAS called and the tokens WERE consumed, so the existing
// post-dispatch metering fires on the real usage unchanged (BR-1.5). A clean
// completion (no confirmed match in any choice) passes through BYTE-IDENTICALLY.
//
// Returns the first Match across redacted choices + whether any redaction
// happened (for the caller's logging / tests). No-op when the output scanner is
// unwired (nil) → pre-8.3 byte-identical behaviour.
func (h *ChatCompletionsHandler) redactResponse(ctx context.Context, resp *ChatResponse) (safetylexicon.Match, bool) {
	if h.outputScanner == nil || resp == nil {
		return safetylexicon.Match{}, false
	}
	var first safetylexicon.Match
	redacted := false
	for i := range resp.Choices {
		m, hit := h.outputScanner.ScanText(resp.Choices[i].Message.Content)
		if !hit {
			continue
		}
		resp.Choices[i].Message.Content = redactedOutputNotice
		resp.Choices[i].FinishReason = "content_filter"
		if !redacted {
			first = m
			redacted = true
		}
		// One event per redacted choice (OQ-8.3-7) — the matched term reaches ONLY
		// the 8.5 seam, never the caller-facing body (no lexicon leak, AC1).
		h.recordSafetyOutputBlock(ctx, m)
	}
	return first, redacted
}

// recordSafetyOutputBlock builds the Story-8.3 OUTPUT interception SafetyEvent
// (direction:"output") from the confirmed Match + the SAME request-context
// identity helpers 8.2 uses, and hands it to the injected Recorder (the Story-8.5
// seam). Mirrors recordSafetyBlock (input) verbatim except DirectionOutput.
// Fire-and-forget; the no-op default persists nothing (zero-DB this story).
func (h *ChatCompletionsHandler) recordSafetyOutputBlock(ctx context.Context, m safetylexicon.Match) {
	if h.safetyRecorder == nil {
		return
	}
	heRequestID, _ := requestid.FromContext(ctx)
	userID, _ := middleware.BearerUserIDFromContext(ctx)
	apiKeyID, _ := middleware.APIKeyIDFromContext(ctx)
	h.safetyRecorder.Record(ctx, contentsafety.SafetyEvent{
		Direction:   contentsafety.DirectionOutput,
		MatchedRule: m.Canonical, // == Match.Canonical (≤100 runes, fits VARCHAR(100))
		Category:    string(m.Category),
		Severity:    string(m.Severity), // carried for 8.5; does NOT gate the 8.3 decision (8.4 owns strictness)
		Action:      contentsafety.ActionBlocked,
		UserID:      userID,
		APIKeyID:    apiKeyID,
		HeRequestID: heRequestID,
	})
}
