// Story 8.3 (AC1/AC3/AC4 non-stream) — internal tests for redactResponse + the
// OUTPUT SafetyEvent. These live in package `handlers` (not handlers_test) so they
// can call the unexported redactResponse directly with a constructed ChatResponse,
// isolating the §9.3 出参 redact LOGIC from the full HTTP path (the wiring at the 3
// write sites is covered end-to-end in chat_completions_safety_output_test.go).
//
// Scenario IDs trace to docs/qa/assessments/8.3-test-design-20260611.md.
package handlers

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/requestid"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// ----- internal helpers -------------------------------------------------

// outRecorder captures OUTPUT SafetyEvents synchronously.
type outRecorder struct {
	mu     sync.Mutex
	events []contentsafety.SafetyEvent
}

func (r *outRecorder) Record(_ context.Context, ev contentsafety.SafetyEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *outRecorder) all() []contentsafety.SafetyEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contentsafety.SafetyEvent(nil), r.events...)
}

// outputHandler builds a handler wired with a DefaultLexicon output scanner (flags
// the known en/abuse/high term "badword") + the supplied recorder.
func outputHandler(rec contentsafety.Recorder) *ChatCompletionsHandler {
	opts := []ChatHandlerOption{WithOutputSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon))}
	if rec != nil {
		opts = append(opts, WithSafetyRecorder(rec))
	}
	return NewChatCompletionsHandler(nil, opts...)
}

// seededOutputHandler builds a handler whose output scanner is the DefaultRegistry
// corpus PLUS the supplied extra rows (used to span categories in the exhaustive
// no-FN test without depending on opaque real corpus terms).
func seededOutputHandler(t *testing.T, rec contentsafety.Recorder, extra ...safetylexicon.Row) *ChatCompletionsHandler {
	t.Helper()
	rows := append([]safetylexicon.Row{}, safetylexicon.DefaultRegistry.Rows...)
	rows = append(rows, extra...)
	lex := safetylexicon.NewFromRegistry(safetylexicon.Registry{Rows: rows})
	opts := []ChatHandlerOption{WithOutputSafetyScanner(contentsafety.NewScanner(lex))}
	if rec != nil {
		opts = append(opts, WithSafetyRecorder(rec))
	}
	return NewChatCompletionsHandler(nil, opts...)
}

// outputCtx carries the bearer/request identity helpers the output event reads.
func outputCtx() context.Context {
	ctx := context.Background()
	ctx = middleware.WithAPIKeyID(ctx, "k-1")
	ctx = middleware.BearerWithUserID(ctx, "u-1")
	ctx = requestid.WithRequestID(ctx, "req_abc")
	return ctx
}

func oneChoiceResp(content string) *ChatResponse {
	return &ChatResponse{
		ID: "chatcmpl-x", Object: "chat.completion", Model: "qwen-max",
		Choices: []ChatChoice{{Index: 0, Message: ChatMessage{Role: "assistant", Content: content}, FinishReason: "stop"}},
		Usage:   ChatUsage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
}

// ----- AC1: non-stream redact + content_filter --------------------------

// 8.3-UNIT-001 — hit → content replaced with the 脱敏 const, finish_reason
// "content_filter", envelope/usage preserved.
func TestRedact_UNIT001_HitRedactsAndSignals(t *testing.T) {
	h := outputHandler(&outRecorder{})
	resp := oneChoiceResp("I really should not say badword in this answer.")
	m, hit := h.redactResponse(outputCtx(), resp)
	if !hit {
		t.Fatal("a sensitive completion was not redacted")
	}
	if m.Canonical != "badword" {
		t.Fatalf("matched_rule = %q, want badword", m.Canonical)
	}
	if resp.Choices[0].Message.Content != redactedOutputNotice {
		t.Fatalf("content = %q, want the 脱敏 notice", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "content_filter" {
		t.Fatalf("finish_reason = %q, want content_filter", resp.Choices[0].FinishReason)
	}
	if resp.ID != "chatcmpl-x" || resp.Object != "chat.completion" {
		t.Fatalf("envelope shape mutated: id=%q object=%q", resp.ID, resp.Object)
	}
}

// 8.3-UNIT-002 — the redacted body carries NO matched substring (no lexicon leak).
func TestRedact_UNIT002_NoLeak(t *testing.T) {
	h := outputHandler(&outRecorder{})
	resp := oneChoiceResp("prefix badword suffix")
	h.redactResponse(outputCtx(), resp)
	if strings.Contains(resp.Choices[0].Message.Content, "badword") {
		t.Fatalf("redacted content leaked the matched term: %q", resp.Choices[0].Message.Content)
	}
}

// 8.3-UNIT-003 / 8.3-UNIT-033 — a clean completion passes through byte-identically;
// finish_reason stays "stop"; no event recorded.
func TestRedact_UNIT003_033_CleanPassthrough(t *testing.T) {
	rec := &outRecorder{}
	h := outputHandler(rec)
	resp := oneChoiceResp("Hello from He-API. A perfectly clean answer.")
	before := *resp
	beforeContent := resp.Choices[0].Message.Content
	m, hit := h.redactResponse(outputCtx(), resp)
	if hit {
		t.Fatalf("clean completion redacted; matched %q", m.Canonical)
	}
	if resp.Choices[0].Message.Content != beforeContent {
		t.Fatal("clean content mutated")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("clean finish_reason = %q, want stop", resp.Choices[0].FinishReason)
	}
	if !reflect.DeepEqual(before.Usage, resp.Usage) {
		t.Fatal("usage mutated on a clean pass")
	}
	if len(rec.all()) != 0 {
		t.Fatalf("clean pass recorded %d events, want 0", len(rec.all()))
	}
}

// 8.3-UNIT-004 — per-choice (A/B): the term in choice[1] only → choice[1] redacted,
// choice[0] untouched, x_he_model preserved on BOTH.
func TestRedact_UNIT004_PerChoiceABIndependence(t *testing.T) {
	h := outputHandler(&outRecorder{})
	resp := &ChatResponse{
		ID: "chatcmpl-ab", Object: "chat.completion",
		Choices: []ChatChoice{
			{Index: 0, Message: ChatMessage{Role: "assistant", Content: "clean leg A output"}, FinishReason: "stop", XHeModel: "model-a"},
			{Index: 1, Message: ChatMessage{Role: "assistant", Content: "leg B says badword"}, FinishReason: "stop", XHeModel: "model-b"},
		},
		Usage: ChatUsage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10},
	}
	h.redactResponse(outputCtx(), resp)

	if resp.Choices[0].Message.Content != "clean leg A output" || resp.Choices[0].FinishReason != "stop" {
		t.Fatal("clean choice[0] was altered")
	}
	if resp.Choices[1].Message.Content != redactedOutputNotice || resp.Choices[1].FinishReason != "content_filter" {
		t.Fatal("sensitive choice[1] not redacted")
	}
	if resp.Choices[0].XHeModel != "model-a" || resp.Choices[1].XHeModel != "model-b" {
		t.Fatalf("x_he_model attribution lost: %q / %q", resp.Choices[0].XHeModel, resp.Choices[1].XHeModel)
	}
}

// 8.3-UNIT-005 / 8.3-UNIT-035 — billing unchanged: resp.Usage is byte-identical
// pre/post redact (body-only mutation, BR-1.5).
func TestRedact_UNIT005_035_BillingUnchanged(t *testing.T) {
	h := outputHandler(&outRecorder{})
	resp := oneChoiceResp("a redactable badword answer")
	want := resp.Usage
	h.redactResponse(outputCtx(), resp)
	if !reflect.DeepEqual(resp.Usage, want) {
		t.Fatalf("usage mutated by redaction: got %+v, want %+v", resp.Usage, want)
	}
}

// 8.3-UNIT-006 / 8.3-BLIND-BOUNDARY-001 — empty / whitespace-only content → no
// candidate, no redaction, no panic.
func TestRedact_UNIT006_EmptyContentNoRedaction(t *testing.T) {
	h := outputHandler(&outRecorder{})
	for _, content := range []string{"", "   ", "\t\n  "} {
		resp := oneChoiceResp(content)
		if _, hit := h.redactResponse(outputCtx(), resp); hit {
			t.Fatalf("empty/whitespace content %q was redacted", content)
		}
		if resp.Choices[0].FinishReason != "stop" {
			t.Fatalf("finish_reason changed for empty content %q", content)
		}
	}
}

// ----- AC3: output event shape ------------------------------------------

// 8.3-UNIT-020 / 8.3-UNIT-021 / 8.3-UNIT-026 — the OUTPUT event carries
// direction="output", action="blocked", matched_rule==Canonical + the ctx ids, and
// carries NO raw model text (excerpt is 8.5's job).
func TestRedact_UNIT020_021_026_OutputEventShape(t *testing.T) {
	rec := &outRecorder{}
	h := outputHandler(rec)
	resp := oneChoiceResp("the model emitted badword unexpectedly")
	h.redactResponse(outputCtx(), resp)

	evs := rec.all()
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want 1", len(evs))
	}
	ev := evs[0]
	if ev.Direction != "output" {
		t.Errorf("direction = %q, want output", ev.Direction)
	}
	if ev.Action != "blocked" {
		t.Errorf("action = %q, want blocked", ev.Action)
	}
	if ev.MatchedRule != "badword" {
		t.Errorf("matched_rule = %q, want badword", ev.MatchedRule)
	}
	if ev.Category != "abuse" || ev.Severity != "high" {
		t.Errorf("category/severity = %q/%q, want abuse/high", ev.Category, ev.Severity)
	}
	if ev.UserID != "u-1" || ev.APIKeyID != "k-1" || ev.HeRequestID != "req_abc" {
		t.Errorf("ctx ids = %q/%q/%q, want u-1/k-1/req_abc", ev.UserID, ev.APIKeyID, ev.HeRequestID)
	}
	// 8.3-UNIT-026 — no raw model text in any event field (excerpt deliberately not
	// populated by 8.3; matched_rule is the canonical term, not the surrounding text).
	for _, f := range []string{ev.MatchedRule, ev.Direction, ev.Action, ev.Category, ev.Severity} {
		if strings.Contains(f, "the model emitted") {
			t.Errorf("event field leaked raw model text: %q", f)
		}
	}
}

// 8.3-UNIT-025 — one event PER REDACTED CHOICE (A/B): 2 redacted choices → 2 events.
func TestRedact_UNIT025_OneEventPerRedactedChoice(t *testing.T) {
	rec := &outRecorder{}
	h := outputHandler(rec)
	resp := &ChatResponse{
		Choices: []ChatChoice{
			{Index: 0, Message: ChatMessage{Content: "leg A badword"}, FinishReason: "stop", XHeModel: "m-a"},
			{Index: 1, Message: ChatMessage{Content: "leg B badword"}, FinishReason: "stop", XHeModel: "m-b"},
		},
	}
	h.redactResponse(outputCtx(), resp)
	if n := len(rec.all()); n != 2 {
		t.Fatalf("recorded %d events for 2 redacted choices, want 2", n)
	}
}

// 8.3-UNIT-023 — the no-op default Recorder still redacts and never panics
// (zero-DB this story). A handler with no WithSafetyRecorder uses NopRecorder.
func TestRedact_UNIT023_NopRecorderDefault(t *testing.T) {
	h := NewChatCompletionsHandler(nil, WithOutputSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)))
	resp := oneChoiceResp("badword here")
	if _, hit := h.redactResponse(outputCtx(), resp); !hit {
		t.Fatal("no-op-recorder default path did not redact")
	}
	if resp.Choices[0].FinishReason != "content_filter" {
		t.Fatal("redaction did not apply under the no-op recorder default")
	}
}

// 8.3-UNIT-030 — exhaustive no-FN (non-stream) across categories: each seeded
// stored term, embedded in a choice, is detected + redacted. (The scanner's
// cross-category/lang no-FN is exhaustively proven in 8.2 scanner_test UNIT-013;
// this asserts redactResponse drives it on every choice.)
func TestRedact_UNIT030_ExhaustiveNoFalseNegative_NonStream(t *testing.T) {
	seeds := []struct {
		raw string
		cat safetylexicon.Category
		sev safetylexicon.Severity
	}{
		{"poloutputterm", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh},
		{"pornoutputterm", safetylexicon.CategoryPornographic, safetylexicon.SeverityHigh},
		{"violoutputterm", safetylexicon.CategoryViolenceTerror, safetylexicon.SeverityMedium},
		{"contraoutputterm", safetylexicon.CategoryContraband, safetylexicon.SeverityMedium},
		{"abuseoutputterm", safetylexicon.CategoryAbuse, safetylexicon.SeverityLow},
		{"otheroutputterm", safetylexicon.CategoryOther, safetylexicon.SeverityLow},
		{"出参敏感词测试甲", safetylexicon.CategoryPolitical, safetylexicon.SeverityHigh},
	}
	rows := make([]safetylexicon.Row, 0, len(seeds))
	for i, s := range seeds {
		lang := safetylexicon.LangEN
		if strings.ContainsRune(s.raw, '出') {
			lang = safetylexicon.LangZH
		}
		rows = append(rows, safetylexicon.Row{Lang: lang, Category: s.cat, Severity: s.sev, Raw: s.raw, File: "test/out/seed.txt", Line: 5000 + i})
	}
	h := seededOutputHandler(t, &outRecorder{}, rows...)
	for _, s := range seeds {
		resp := oneChoiceResp("noise prefix " + s.raw + " noise suffix")
		m, hit := h.redactResponse(outputCtx(), resp)
		if !hit {
			t.Fatalf("FALSE-NEGATIVE (compliance breach): term %q (cat=%s) not redacted in a non-stream choice", s.raw, s.cat)
		}
		if m.Canonical != safetylexicon.Normalize(s.raw) {
			t.Fatalf("term %q reported wrong canonical %q", s.raw, m.Canonical)
		}
	}
}

// 8.3-UNIT — output scanner unwired (nil) → redactResponse is a no-op (pre-8.3
// byte-identical behaviour; a handler built without WithOutputSafetyScanner).
func TestRedact_ScannerUnwired_NoOp(t *testing.T) {
	h := NewChatCompletionsHandler(nil) // no output scanner
	resp := oneChoiceResp("contains badword but no scanner wired")
	if _, hit := h.redactResponse(outputCtx(), resp); hit {
		t.Fatal("redactResponse fired with no output scanner wired")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Fatal("unwired handler mutated the response")
	}
}
