// Story 8.2 — handler-level content-safety tests (AC1 reject-before-dispatch +
// canonical envelope, AC3 handler-built interception event, AC4 no-charge-on-
// block, integration + flow regressions).
//
// Scenario IDs trace to docs/qa/assessments/8.2-test-design-20260610.md.
package handlers_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// ----- helpers ----------------------------------------------------------

// captureRecorder records SafetyEvents synchronously (the handler calls Record
// inline on the reject path).
type captureRecorder struct {
	mu     sync.Mutex
	events []contentsafety.SafetyEvent
}

func (r *captureRecorder) Record(_ context.Context, ev contentsafety.SafetyEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *captureRecorder) all() []contentsafety.SafetyEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contentsafety.SafetyEvent(nil), r.events...)
}

// safetyHandler builds a handler wired with the production DefaultLexicon scanner
// + the supplied recorder (nil → NopRecorder default), plus any extra options.
func safetyHandler(rec contentsafety.Recorder, opts ...handlers.ChatHandlerOption) *handlers.ChatCompletionsHandler {
	base := []handlers.ChatHandlerOption{
		handlers.WithSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)),
	}
	if rec != nil {
		base = append(base, handlers.WithSafetyRecorder(rec))
	}
	base = append(base, opts...)
	return handlers.NewChatCompletionsHandler(discardLogger(), base...)
}

// badword is a known stored term (en/abuse/high) in the DefaultLexicon corpus.
const (
	hitUserBody      = `{"model":"qwen-max","messages":[{"role":"user","content":"please say badword now"}]}`
	hitSystemBody    = `{"model":"qwen-max","messages":[{"role":"system","content":"system rules: badword policy"},{"role":"user","content":"hello"}]}`
	hitAssistantBody = `{"model":"qwen-max","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"sure, badword"},{"role":"user","content":"ok"}]}`
	cleanStreamBody  = `{"model":"qwen-max","stream":true,"messages":[{"role":"user","content":"write a short clean greeting"}]}`
)

// ----- AC1: interception + canonical 400_content_filter envelope --------

// 8.2-UNIT-001 — hit → canonical §5.1.2 envelope: code, type, param=null,
// he_request_id present, HTTP 400.
func TestSafety_UNIT001_EnvelopeShape(t *testing.T) {
	rr := doRequest(t, safetyHandler(&captureRecorder{}), hitUserBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	errObj, ok := decodeBody(t, rr)["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in body: %s", rr.Body.String())
	}
	if errObj["code"] != "400_content_filter" {
		t.Errorf("code = %v, want 400_content_filter", errObj["code"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("type = %v, want invalid_request_error", errObj["type"])
	}
	if v, present := errObj["param"]; !present || v != nil {
		t.Errorf("param = %v (present=%v), want JSON null", v, present)
	}
	if s, _ := errObj["he_request_id"].(string); s == "" {
		t.Error("he_request_id missing/empty")
	}
}

// 8.2-UNIT-002 — the reject PRECEDES dispatch: routing-svc is not consulted and
// no adapter handle is invoked on a hit.
func TestSafety_UNIT002_RejectPrecedesDispatch(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": fh})
	emit := &recordingEmitter{}
	dd := &countingDeducter{}

	h := safetyHandler(&captureRecorder{},
		handlers.WithRouter(routingclient.NewDecider(rc, nil)),
		handlers.WithAdapterRegistry(reg),
		handlers.WithUsageEmitter(emit),
		handlers.WithTokenDeducter(dd),
	)
	rr := doRequest(t, h, hitUserBody)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if rc.gotReq != nil {
		t.Error("routing-svc was consulted on a blocked request (should reject before routing)")
	}
	if fh.called != 0 {
		t.Errorf("adapter dispatched %d times on a blocked request, want 0", fh.called)
	}
	if n := len(emit.all()); n != 0 {
		t.Errorf("usage emitted %d events on a blocked request, want 0", n)
	}
	if dd.calls != 0 {
		t.Errorf("token deducter called %d times on a blocked request, want 0", dd.calls)
	}
}

// 8.2-UNIT-003 — no lexicon leak: param is null AND the matched substring never
// appears in the response body.
func TestSafety_UNIT003_NoLeak(t *testing.T) {
	rr := doRequest(t, safetyHandler(&captureRecorder{}), hitUserBody)
	body := rr.Body.String()
	if strings.Contains(body, "badword") {
		t.Errorf("response body leaked the matched term: %s", body)
	}
	errObj := decodeBody(t, rr)["error"].(map[string]any)
	if errObj["param"] != nil {
		t.Errorf("param = %v, want null (no leak surface)", errObj["param"])
	}
}

// 8.2-UNIT-004 / 8.2-BLIND-FLOW-001 — every role is scanned: a term in a
// system / assistant (non-first, non-user) message still blocks.
func TestSafety_UNIT004_FLOW001_AllRolesScanned(t *testing.T) {
	for name, body := range map[string]string{"system": hitSystemBody, "assistant": hitAssistantBody} {
		t.Run(name, func(t *testing.T) {
			rr := doRequest(t, safetyHandler(&captureRecorder{}), body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (term in %s message must block); body=%s", rr.Code, name, rr.Body.String())
			}
			if decodeBody(t, rr)["error"].(map[string]any)["code"] != "400_content_filter" {
				t.Errorf("%s-message hit not blocked by content filter", name)
			}
		})
	}
}

// 8.2-UNIT-005 — clean passthrough is BYTE-IDENTICAL to the pre-8.2 path (no
// scanner). Deterministic id + clock so the two bodies are comparable.
func TestSafety_UNIT005_CleanPassthroughByteIdentity(t *testing.T) {
	id := func() string { return "chatcmpl-mock-deadbeef0001" }
	clk := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

	withScan := safetyHandler(nil, handlers.WithIDFactory(id), handlers.WithNow(clk))
	noScan := handlers.NewChatCompletionsHandler(discardLogger(), handlers.WithIDFactory(id), handlers.WithNow(clk))

	rrA := doRequest(t, withScan, validReqBody)
	rrB := doRequest(t, noScan, validReqBody)

	if rrA.Code != http.StatusOK || rrB.Code != http.StatusOK {
		t.Fatalf("status with-scan=%d no-scan=%d, want both 200", rrA.Code, rrB.Code)
	}
	if !bytes.Equal(rrA.Body.Bytes(), rrB.Body.Bytes()) {
		t.Fatalf("clean-path body diverged:\n with-scan=%s\n no-scan  =%s", rrA.Body.String(), rrB.Body.String())
	}
}

// ----- AC3: handler-built interception event ----------------------------

// 8.2-UNIT-020 / 8.2-UNIT-021 — the handler builds the full SafetyEvent (all
// fields) and matched_rule == Match.Canonical (≤100 runes).
func TestSafety_UNIT020_021_EventShape(t *testing.T) {
	rec := &captureRecorder{}
	rr := doRequest(t, safetyHandler(rec), hitUserBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	evs := rec.all()
	if len(evs) != 1 {
		t.Fatalf("recorded %d events, want exactly 1", len(evs))
	}
	ev := evs[0]
	if ev.Direction != "input" {
		t.Errorf("direction = %q, want input", ev.Direction)
	}
	if ev.Action != "blocked" {
		t.Errorf("action = %q, want blocked", ev.Action)
	}
	if ev.MatchedRule != "badword" { // == Match.Canonical for the en/abuse term
		t.Errorf("matched_rule = %q, want badword (Match.Canonical)", ev.MatchedRule)
	}
	if len([]rune(ev.MatchedRule)) > 100 {
		t.Errorf("matched_rule = %d runes, exceeds VARCHAR(100)", len([]rune(ev.MatchedRule)))
	}
	if ev.Category != "abuse" {
		t.Errorf("category = %q, want abuse", ev.Category)
	}
	if ev.Severity != "high" {
		t.Errorf("severity = %q, want high", ev.Severity)
	}
	if ev.UserID != testUserID {
		t.Errorf("user_id = %q, want %q", ev.UserID, testUserID)
	}
	if ev.APIKeyID != testAPIKeyID {
		t.Errorf("api_key_id = %q, want %q", ev.APIKeyID, testAPIKeyID)
	}
}

// 8.2-UNIT-022 (handler half) — the no-op default Recorder still 400s the request
// and never panics (zero-DB this story; persistence is Story 8.5).
func TestSafety_UNIT022_NopRecorderDefault(t *testing.T) {
	// No WithSafetyRecorder → constructor installs contentsafety.NopRecorder.
	h := handlers.NewChatCompletionsHandler(discardLogger(),
		handlers.WithSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)))
	rr := doRequest(t, h, hitUserBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 with the no-op recorder default", rr.Code)
	}
	if decodeBody(t, rr)["error"].(map[string]any)["code"] != "400_content_filter" {
		t.Error("no-op default path did not block")
	}
}

// ----- AC4: no-charge-on-block + integration ----------------------------

// 8.2-UNIT-031 — a blocked request meters nothing: usage-emit and billing-charge
// call-counts are both 0.
func TestSafety_UNIT031_NoChargeOnBlock(t *testing.T) {
	emit := &recordingEmitter{}
	dd := &countingDeducter{}
	h := safetyHandler(&captureRecorder{}, handlers.WithUsageEmitter(emit), handlers.WithTokenDeducter(dd))
	rr := doRequest(t, h, hitUserBody)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if n := len(emit.all()); n != 0 {
		t.Errorf("usage emitted %d events on block, want 0", n)
	}
	if dd.calls != 0 {
		t.Errorf("token deducter called %d times on block, want 0", dd.calls)
	}
}

// 8.2-INT-001 — full handler path: authenticated POST with a sensitive term →
// 400, no dispatch, event recorded to the fake Recorder.
func TestSafety_INT001_BlockedRequestEndToEnd(t *testing.T) {
	rc := &fakeRoutingClient{resp: routedResp("deepseek-v3", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	fh := newSingleChunkHandle(canonicalAdapterChunk())
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"deepseek-v3": fh})
	rec := &captureRecorder{}
	h := safetyHandler(rec,
		handlers.WithRouter(routingclient.NewDecider(rc, nil)),
		handlers.WithAdapterRegistry(reg),
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(hitUserBody))
	req.Header.Set("Authorization", "Bearer he-test-key-stub")
	req = req.WithContext(withBearerCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if fh.called != 0 || rc.gotReq != nil {
		t.Errorf("upstream touched on block: adapter.called=%d routing.gotReq=%v", fh.called, rc.gotReq)
	}
	if len(rec.all()) != 1 {
		t.Errorf("recorded %d events, want 1", len(rec.all()))
	}
}

// 8.2-INT-002 — full handler path: clean authenticated POST → normal 200 path,
// no event recorded.
func TestSafety_INT002_CleanRequestEndToEnd(t *testing.T) {
	rec := &captureRecorder{}
	rr := doRequest(t, safetyHandler(rec), validReqBody)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("clean request recorded %d events, want 0", n)
	}
}

// 8.2-BLIND-FLOW-003 — a clean stream:true request is not blocked by the scanner;
// the streaming path is unchanged (regression guard at the seam).
func TestSafety_BLIND_FLOW003_CleanStreamUnchanged(t *testing.T) {
	rec := &captureRecorder{}
	rr := doRequest(t, safetyHandler(rec), cleanStreamBody)
	if rr.Code == http.StatusBadRequest {
		t.Fatalf("clean stream request was blocked: %s", rr.Body.String())
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("clean stream recorded %d events, want 0", n)
	}
}

// 8.2-INT-003 — BR-4.2: the gateway + safety-lexicon CI lanes already run these
// tests under -race; Story 8.2 adds NO new lane. Confirm both invocations are
// present in the workflow (don't add one).
func TestSafety_INT003_CILanesPresent(t *testing.T) {
	const wf = "../../../../.github/workflows/test.yml"
	b, err := os.ReadFile(wf)
	if err != nil {
		t.Fatalf("read %s: %v", wf, err)
	}
	yml := string(b)
	for _, want := range []string{
		"go test ./apps/api-gateway/... -count=1 -race",
		"go test ./packages/safety-lexicon/... -count=1 -race",
	} {
		if !strings.Contains(yml, want) {
			t.Errorf("test.yml missing required -race lane invocation: %q", want)
		}
	}
}
