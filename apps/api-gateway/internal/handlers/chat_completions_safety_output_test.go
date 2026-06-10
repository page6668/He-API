// Story 8.3 — handler-level §9.3 出参 integration tests (HTTP path): the 3
// non-stream write sites (mock / adapter / A/B), the streaming guard seam, billing
// invariants (M-2), and the cross-direction regression (8.2 input NOT weakened).
//
// Scenario IDs trace to docs/qa/assessments/8.3-test-design-20260611.md.
package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// defaultOutputScanner flags the known en/abuse/high term "badword".
func defaultOutputScanner() *contentsafety.Scanner {
	return contentsafety.NewScanner(safetylexicon.DefaultLexicon)
}

// seededOutputScanner = the DefaultRegistry corpus + one extra en term (used to
// flag a word that appears in the fixed MockContent so the mock write site can be
// exercised end-to-end).
func seededOutputScanner(t *testing.T, raw string) *contentsafety.Scanner {
	t.Helper()
	rows := append([]safetylexicon.Row{}, safetylexicon.DefaultRegistry.Rows...)
	rows = append(rows, safetylexicon.Row{Lang: safetylexicon.LangEN, Category: safetylexicon.CategoryOther, Severity: safetylexicon.SeverityLow, Raw: raw, File: "test/out/seed.txt", Line: 8001})
	return contentsafety.NewScanner(safetylexicon.NewFromRegistry(safetylexicon.Registry{Rows: rows}))
}

// sensitiveAdapterChunk is the terminal non-stream ChatChunk with a sensitive
// completion (carries the known term "badword") + real usage (total 13).
func sensitiveAdapterChunk() *adapterv1.ChatChunk {
	role := "assistant"
	content := "sure, here is badword for you"
	stop := "stop"
	return &adapterv1.ChatChunk{
		Id:           "chatcmpl-real-bad",
		Object:       "chat.completion",
		Created:      1700000000,
		Model:        "deepseek-v3",
		Choices:      []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role, Content: &content}, FinishReason: &stop}},
		Usage:        &adapterv1.Usage{PromptTokens: 5, CompletionTokens: 8, TotalTokens: 13},
		FinishReason: &stop,
	}
}

// ----- AC1/AC3 — non-stream adapter write site ----------------------------

// 8.3-INT-001 — authenticated non-stream POST whose upstream returns a sensitive
// completion → 200 with content redacted + finish_reason="content_filter"; output
// event recorded; TPMDeduct fires on the REAL usage (billing unchanged, BR-1.5).
func TestSafetyOutput_INT001_NonStreamAdapterRedactEndToEnd(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": newSingleChunkHandle(sensitiveAdapterChunk())})
	rec := &captureRecorder{}
	dd := &countingDeducter{}
	h := failoverHandler(t, rc, reg, dd,
		handlers.WithOutputSafetyScanner(defaultOutputScanner()),
		handlers.WithSafetyRecorder(rec),
	)

	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}]}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"finish_reason":"content_filter"`) {
		t.Fatalf("missing content_filter finish_reason; body=%s", body)
	}
	if strings.Contains(body, "badword") {
		t.Fatalf("redacted body leaked the matched term: %s", body)
	}
	evs := rec.all()
	if len(evs) != 1 || evs[0].Direction != "output" || evs[0].Action != "blocked" || evs[0].MatchedRule != "badword" {
		t.Fatalf("output event = %+v, want 1 {direction:output, action:blocked, matched_rule:badword}", evs)
	}
	// Billing unchanged: TPMDeduct fired on the REAL total_tokens (13), not altered.
	if dd.calls != 1 || dd.lastTokens != 13 {
		t.Fatalf("TPMDeduct calls=%d tokens=%d, want 1 / 13 (real usage, redaction is body-only)", dd.calls, dd.lastTokens)
	}
}

// 8.3-INT-002 (mock site) — the mock write site redacts: an output scanner that
// flags a word in the fixed MockContent → mock 200 redacted + content_filter.
func TestSafetyOutput_INT002_MockWriteSiteRedacts(t *testing.T) {
	rec := &captureRecorder{}
	// MockContent contains the word "mock"; flag it so the mock body redacts.
	h := handlers.NewChatCompletionsHandler(discardLogger(),
		handlers.WithOutputSafetyScanner(seededOutputScanner(t, "mock")),
		handlers.WithSafetyRecorder(rec),
	)
	rr := doRequest(t, h, validReqBody) // qwen-max → unregistered → mock path
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"finish_reason":"content_filter"`) {
		t.Fatalf("mock write site did not redact; body=%s", rr.Body.String())
	}
	if len(rec.all()) != 1 {
		t.Fatalf("mock redact recorded %d events, want 1", len(rec.all()))
	}
}

// 8.3-INT-002 (A/B site) / 8.3-UNIT-025 e2e — the A/B merge write site redacts each
// sensitive choice independently → 2 events, x_he_model preserved.
func TestSafetyOutput_INT002_ABWriteSiteRedactsPerChoice(t *testing.T) {
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"model-a": newSingleChunkHandle(sensitiveAdapterChunk()),
		"model-b": newSingleChunkHandle(sensitiveAdapterChunk()),
	})
	rec := &captureRecorder{}
	h := abHandler(t, reg, &countingDeducter{},
		handlers.WithOutputSafetyScanner(defaultOutputScanner()),
		handlers.WithSafetyRecorder(rec),
	)

	rr := doABRequest(t, h, "model-a,model-b", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "badword") {
		t.Fatalf("A/B body leaked the matched term: %s", rr.Body.String())
	}
	if n := strings.Count(rr.Body.String(), `"finish_reason":"content_filter"`); n != 2 {
		t.Fatalf("A/B redacted %d choices, want 2; body=%s", n, rr.Body.String())
	}
	if len(rec.all()) != 2 {
		t.Fatalf("A/B redact recorded %d events, want 2 (one per redacted choice)", len(rec.all()))
	}
}

// ----- AC2 — streaming write path -----------------------------------------

// sensitiveStreamHandleEarlyUsage streams a clean prefix (carrying usage so the
// tail-usage capture has data) then a delta completing "badword".
func sensitiveStreamHandleEarlyUsage() *fakeHandle {
	role := "assistant"
	clean := "clean prefix "
	bad := "badword"
	chunks := []*adapterv1.ChatChunk{
		{Id: "s", Object: "chat.completion.chunk", Created: 1700000000, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role}}}},
		{Id: "s", Object: "chat.completion.chunk", Created: 1700000000, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &clean}}},
			Usage:   &adapterv1.Usage{PromptTokens: 5, CompletionTokens: 99, TotalTokens: 104}},
		{Id: "s", Object: "chat.completion.chunk", Created: 1700000000, Model: "deepseek-v3",
			Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Content: &bad}}}},
	}
	return &fakeHandle{resp: &fakeStream{chunks: chunks}}
}

// 8.3-INT-010/014 + 8.3-UNIT-036 (M-2) — a streaming request whose upstream emits a
// sensitive term terminates with a content_filter terminal + [DONE]; output event
// recorded; partial TPM deduct of the CONSUMED tail (prompt_tokens=5, NOT total
// 104, NOT skipped, NOT doubled).
func TestSafetyOutput_INT010_036_StreamFilteredEndToEnd(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": sensitiveStreamHandleEarlyUsage()})
	rec := &captureRecorder{}
	dd := &countingDeducter{}
	h := failoverHandler(t, rc, reg, dd,
		handlers.WithOutputSafetyScanner(defaultOutputScanner()),
		handlers.WithSafetyRecorder(rec),
	)

	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}],"stream":true}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "content_filter") {
		t.Fatalf("stream did not emit a content_filter terminal; body=%s", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("filtered stream missing [DONE]; body=%s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Fatalf("filtered stream emitted an error frame (must be a normal terminal, M-1); body=%s", body)
	}
	if strings.Contains(body, "badword") {
		t.Fatalf("filtered stream leaked the matched term; body=%s", body)
	}
	if len(rec.all()) != 1 || rec.all()[0].Direction != "output" {
		t.Fatalf("stream output event = %+v, want 1 direction:output", rec.all())
	}
	// M-2 — ErrContentFiltered routes to the partial billing arm: deduct the
	// consumed prompt_tokens (5), NOT the total (104), exactly once.
	if dd.calls != 1 || dd.lastTokens != 5 {
		t.Fatalf("stream TPMDeduct calls=%d tokens=%d, want 1 / 5 (partial consumed tail, M-2)", dd.calls, dd.lastTokens)
	}
}

// 8.3-INT-013 — a CLEAN stream is unaffected by the output guard: normal stop
// terminal + [DONE], no content_filter, full TPM deduct on total.
func TestSafetyOutput_INT013_CleanStreamUnaffected(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": streamingOKHandle()})
	dd := &countingDeducter{}
	h := failoverHandler(t, rc, reg, dd, handlers.WithOutputSafetyScanner(defaultOutputScanner()))

	rr := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}],"stream":true}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "content_filter") {
		t.Fatalf("clean stream wrongly filtered; body=%s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("clean stream shape changed; body=%s", body)
	}
}

// ----- AC4 — cross-direction regression (8.2 input NOT weakened) ----------

// 8.3-INT-020 — an INPUT hit still returns 400_content_filter (8.2 unchanged); an
// OUTPUT hit returns 200-with-content_filter (8.3). The two directions coexist on
// one handler with BOTH scanners wired.
func TestSafetyOutput_INT020_CrossDirection(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": newSingleChunkHandle(sensitiveAdapterChunk())})
	h := failoverHandler(t, rc, reg, &countingDeducter{},
		handlers.WithSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)), // 8.2 input
		handlers.WithOutputSafetyScanner(defaultOutputScanner()),                           // 8.3 output
		handlers.WithSafetyRecorder(&captureRecorder{}),
	)

	// INPUT hit → 400 (8.2 path NOT weakened by 8.3).
	in := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"say badword"}]}`, "")
	if in.Code != http.StatusBadRequest {
		t.Fatalf("input hit status = %d, want 400 (8.2 must still reject); body=%s", in.Code, in.Body.String())
	}
	if decodeBody(t, in)["error"].(map[string]any)["code"] != "400_content_filter" {
		t.Fatalf("input hit not 400_content_filter; body=%s", in.Body.String())
	}

	// OUTPUT hit (clean input, sensitive upstream completion) → 200 content_filter.
	out := doRoutedRequest(t, h, `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}]}`, "")
	if out.Code != http.StatusOK {
		t.Fatalf("output hit status = %d, want 200 (output filter is success-but-filtered); body=%s", out.Code, out.Body.String())
	}
	if !strings.Contains(out.Body.String(), `"finish_reason":"content_filter"`) {
		t.Fatalf("output hit did not redact; body=%s", out.Body.String())
	}
}

// 8.3-INT-021 — cross-path: the SAME term redacts a non-stream choice AND
// terminates a stream; matched_rule == Match.Canonical ("badword") in both events.
func TestSafetyOutput_INT021_CrossPathSameTerm(t *testing.T) {
	mk := func(stream bool, handle adapterclient.ClientHandle) *captureRecorder {
		rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
		reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": handle})
		rec := &captureRecorder{}
		h := failoverHandler(t, rc, reg, &countingDeducter{},
			handlers.WithOutputSafetyScanner(defaultOutputScanner()), handlers.WithSafetyRecorder(rec))
		body := `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}]}`
		if stream {
			body = `{"model":"deepseek-v3","messages":[{"role":"user","content":"hi"}],"stream":true}`
		}
		doRoutedRequest(t, h, body, "")
		return rec
	}
	nonStream := mk(false, newSingleChunkHandle(sensitiveAdapterChunk()))
	streamRec := mk(true, sensitiveStreamHandleEarlyUsage())

	if len(nonStream.all()) != 1 || nonStream.all()[0].MatchedRule != "badword" {
		t.Fatalf("non-stream event = %+v, want matched_rule badword", nonStream.all())
	}
	if len(streamRec.all()) != 1 || streamRec.all()[0].MatchedRule != "badword" {
		t.Fatalf("stream event = %+v, want matched_rule badword", streamRec.all())
	}
}
