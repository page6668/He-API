// Story 10.6 — GATEWAY surface tests for POST /v1/me/playground/chat.
//
// AUTO-GENERATED skeleton by QA Test Design (Turing, 2026-06-16); implemented by
// Dev. The file is `handlers_test` (external) — not the skeleton's `handlers` —
// so it can reuse the rich chat-handler harness (countingDeducter, fakeHandle,
// legHandle, NewRegistryFromHandles, passthrough router) defined across the other
// handlers_test files. Each scenario maps to docs/qa/assessments/10.6-test-design-20260616.md.
//
// Design under test: PlaygroundChatHandler resolves a body-carried api_key_id to
// the JWT user's OWN per-key policy (IDOR fence), injects the bearer-style
// context, and delegates to the SAME chat pipeline. So:
//   - fence/auth/validation scenarios drive a recording stub chat handler;
//   - dispatch/billing/A-B/scope/safety scenarios drive the REAL
//     ChatCompletionsHandler + stub adapters + a counting deducter;
//   - error-passthrough scenarios (esp. 402, which only the billing gate emits —
//     never an upstream) drive a stub chat handler that writes the envelope.
package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/openaierr"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	safetylexicon "github.com/he-api/he-api/packages/safety-lexicon"
)

// playgroundKeyID is a valid RFC-4122 v4 UUID (version nibble 4, variant 8) — the
// shared-test testAPIKeyID is NOT v4, and uuidV4Re rejects it.
const playgroundKeyID = "33333333-3333-4333-8333-333333333333"

// ----- harness ---------------------------------------------------------------

// stubResolver is a fixed PlaygroundKeyResolver: returns `policy` (or `err`).
type stubResolver struct {
	policy *handlers.ResolvedKeyPolicy
	err    error
}

func (s *stubResolver) Resolve(_ context.Context, _, apiKeyID string) (*handlers.ResolvedKeyPolicy, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.policy, nil
}

// ownedResolver resolves any api_key_id to a policy with the given scope.models.
func ownedResolver(scope []string) handlers.PlaygroundKeyResolver {
	return &stubResolver{policy: &handlers.ResolvedKeyPolicy{APIKeyID: playgroundKeyID, ScopeModels: scope}}
}

// recordingChat is a stub chat handler. It records the injected bearer context +
// forwarded body, and either streams an SSE frame, writes a canned error
// envelope, or writes a 200 echoing the resolved api_key_id.
type recordingChat struct {
	called      int
	gotAPIKeyID string
	gotUserID   string
	gotBody     string
	sse         bool
	errStatus   int    // if >0, write openaierr envelope with errCode
	errCode     string
}

func (c *recordingChat) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.called++
	c.gotAPIKeyID, _ = middleware.APIKeyIDFromContext(r.Context())
	c.gotUserID, _ = middleware.BearerUserIDFromContext(r.Context())
	b, _ := io.ReadAll(r.Body)
	c.gotBody = string(b)
	switch {
	case c.sse:
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-x\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	case c.errStatus > 0:
		_ = openaierr.Write(w, r.Context(), c.errStatus, c.errCode, "stub envelope", nil)
	default:
		w.WriteHeader(http.StatusOK)
		// echo the per-request api_key_id so the concurrency fence is observable.
		_, _ = w.Write([]byte(`{"seen_api_key_id":"` + c.gotAPIKeyID + `","seen_user_id":"` + c.gotUserID + `"}`))
	}
}

// realChat builds the production chat pipeline with a passthrough router + stub
// adapters + (optional) counting deducter + extra options.
func realChat(reg *adapterclient.Registry, dd handlers.TokenDeducter, opts ...handlers.ChatHandlerOption) *handlers.ChatCompletionsHandler {
	base := []handlers.ChatHandlerOption{
		handlers.WithRouter(routingclient.NewDecider(nil, nil)),
		handlers.WithAdapterRegistry(reg),
	}
	if dd != nil {
		base = append(base, handlers.WithTokenDeducter(dd))
	}
	base = append(base, opts...)
	return handlers.NewChatCompletionsHandler(nil, base...)
}

// playgroundBodyJSON builds a single-model request body carrying api_key_id.
func playgroundBodyJSON(model string) string {
	return `{"api_key_id":"` + playgroundKeyID + `","model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
}

// doPlaygroundAs invokes the handler with the given JWT user (empty → no JWT
// context) and optional X-He-AB-Models header.
func doPlaygroundAs(h *handlers.PlaygroundChatHandler, userID, body, abHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/me/playground/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if abHeader != "" {
		req.Header.Set(routingclient.ABModelsHeader, abHeader)
	}
	if userID != "" {
		req = req.WithContext(middleware.WithUserID(req.Context(), userID))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func doPlayground(h *handlers.PlaygroundChatHandler, body, abHeader string) *httptest.ResponseRecorder {
	return doPlaygroundAs(h, testUserID, body, abHeader)
}

// errCodeOf extracts error.code from a §5.1.2 envelope body.
func errCodeOf(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v; body=%s", err, rr.Body.String())
	}
	return env.Error.Code
}

// ----- AC1.A — gateway endpoint security/billing core ------------------------

func Test_10_6_INT_001_MissingJWT_401(t *testing.T) {
	// Pri: P0 [SEC] — no JWT `sub` in context → 401, chat never delegated.
	stub := &recordingChat{}
	h := handlers.NewPlaygroundChatHandler(stub, ownedResolver(nil), nil)
	rr := doPlaygroundAs(h, "", playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "401_unauthenticated" {
		t.Errorf("code = %q, want 401_unauthenticated", got)
	}
	if stub.called != 0 {
		t.Errorf("chat delegated %d times, want 0 (fenced before dispatch)", stub.called)
	}
}

func Test_10_6_INT_002_ForeignApiKeyID_403_IDORFence(t *testing.T) {
	// Pri: P0 [SEC] — resolver reports the key is not the user's → 403, no dispatch.
	stub := &recordingChat{}
	h := handlers.NewPlaygroundChatHandler(stub, &stubResolver{err: handlers.ErrKeyNotOwned}, nil)
	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "403_api_key_not_owned" {
		t.Errorf("code = %q, want 403_api_key_not_owned", got)
	}
	if stub.called != 0 {
		t.Errorf("chat delegated %d times, want 0 (IDOR fence before dispatch)", stub.called)
	}
}

func Test_10_6_INT_003_OwnedKey_InternalDispatch_BilledOnce(t *testing.T) {
	// Pri: P0 — owned key resolved → real internal dispatch → billed ONCE,
	// attributed to the resolved api_key_id (no new per-user path).
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": newSingleChunkHandle(canonicalAdapterChunk()),
	})
	dd := &countingDeducter{}
	h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)

	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want 1 (billed once)", dd.calls)
	}
	if dd.lastKey != playgroundKeyID {
		t.Errorf("billed api_key_id = %q, want %q (resolved key)", dd.lastKey, playgroundKeyID)
	}
	if dd.lastTokens != 13 { // canonicalAdapterChunk usage total
		t.Errorf("billed tokens = %d, want 13", dd.lastTokens)
	}
}

func Test_10_6_INT_004_NonStream_200_NoStore(t *testing.T) {
	// Pri: P0 — OpenAI 200 envelope + Cache-Control: no-store.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": newSingleChunkHandle(canonicalAdapterChunk()),
	})
	h := handlers.NewPlaygroundChatHandler(realChat(reg, &countingDeducter{}), ownedResolver(nil), nil)

	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var b struct {
		Object string `json:"object"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &b)
	if b.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", b.Object)
	}
}

func Test_10_6_INT_005_Streaming_SSE_Passthrough(t *testing.T) {
	// Pri: P0 — the delegating proxy must not buffer/break the SSE stream
	// (data:<json>\n\n ... data:[DONE]\n\n, §5.1.1).
	stub := &recordingChat{sse: true}
	h := handlers.NewPlaygroundChatHandler(stub, ownedResolver(nil), nil)
	body := `{"api_key_id":"` + playgroundKeyID + `","model":"qwen-max","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rr := doPlayground(h, body, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	out := rr.Body.String()
	if !strings.Contains(out, "data: ") || !strings.Contains(out, "data: [DONE]\n\n") {
		t.Errorf("SSE frames not passed through verbatim: %q", out)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

func Test_10_6_INT_006_ModelNotInScope_403(t *testing.T) {
	// Pri: P0 [SEC] — single-model model ∉ scope.models → 403, no dispatch.
	stub := &recordingChat{}
	h := handlers.NewPlaygroundChatHandler(stub, ownedResolver([]string{"deepseek-v3"}), nil)
	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "403_model_not_in_scope" {
		t.Errorf("code = %q, want 403_model_not_in_scope", got)
	}
	if stub.called != 0 {
		t.Errorf("chat delegated %d times, want 0 (scope gate before dispatch)", stub.called)
	}
}

func Test_10_6_INT_007_StrictDecode_ForgedUserID_400(t *testing.T) {
	// Pri: P0 [SEC] — a body carrying user_id (or any unknown field) → 400; the
	// identity is the JWT sub ONLY (9.3 BR-EX-1). Chat never delegated.
	stub := &recordingChat{}
	h := handlers.NewPlaygroundChatHandler(stub, ownedResolver(nil), nil)
	body := `{"api_key_id":"` + playgroundKeyID + `","user_id":"44444444-4444-4444-4444-444444444444","model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`
	rr := doPlayground(h, body, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "400_invalid_request" {
		t.Errorf("code = %q, want 400_invalid_request", got)
	}
	if stub.called != 0 {
		t.Errorf("chat delegated %d times, want 0 (strict-decode reject)", stub.called)
	}
}

func Test_10_6_INT_009_AB_ExactlyTwo_BothLegsBilled(t *testing.T) {
	// Pri: P0 — A/B header honored end-to-end; both legs billed; usage accumulated.
	legA := legHandle("cmpl-a", "qwen-max", "A-answer", 10, 20)
	legB := legHandle("cmpl-b", "deepseek-v3", "B-answer", 30, 40)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)

	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "qwen-max,deepseek-v3")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var b struct {
		Choices []struct {
			XHeModel string `json:"x_he_model"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rr.Body.String())
	}
	if len(b.Choices) != 2 {
		t.Fatalf("choices = %d, want 2", len(b.Choices))
	}
	if b.Usage.TotalTokens != 100 {
		t.Errorf("usage.total = %d, want 100 (summed)", b.Usage.TotalTokens)
	}
	if dd.calls != 1 || dd.lastTokens != 100 {
		t.Errorf("TPMDeduct calls=%d tokens=%d, want 1 call of 100 (dual-billing)", dd.calls, dd.lastTokens)
	}
}

func Test_10_6_INT_010_AB_PlusStream_400(t *testing.T) {
	// Pri: P0 — A/B is non-streaming only (6.4 Q-B); stream=true + A/B → 400.
	legA := legHandle("cmpl-a", "qwen-max", "A", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": legA, "deepseek-v3": legA})
	dd := &countingDeducter{}
	h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)

	body := `{"api_key_id":"` + playgroundKeyID + `","model":"qwen-max","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rr := doPlayground(h, body, "qwen-max,deepseek-v3")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0 (rejected before dispatch)", dd.calls)
	}
}

func Test_10_6_INT_011_AB_LegOutOfScope_403(t *testing.T) {
	// Pri: P0 [SEC] — an A/B leg ∉ scope → whole request 403 (6.4 Q-K), inside the
	// reused dispatchAB (reads ScopeModels from the playground-injected claims).
	legA := legHandle("cmpl-a", "qwen-max", "A", 1, 1)
	legB := legHandle("cmpl-b", "deepseek-v3", "B", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver([]string{"qwen-max"}), nil)

	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "qwen-max,deepseek-v3")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "403_model_not_in_scope" {
		t.Errorf("code = %q, want 403_model_not_in_scope", got)
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0", dd.calls)
	}
}

func Test_10_6_INT_012_AB_PartialFailure_200_Marker(t *testing.T) {
	// Pri: P1 — one leg fails → 200 + failed-leg marker; only the OK leg billed.
	legA := legHandle("cmpl-a", "qwen-max", "A", 10, 20)
	legB := &failingHandle{code: connect.CodeUnavailable}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)

	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "qwen-max,deepseek-v3")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var b struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			XHeError     *struct {
				Code string `json:"code"`
			} `json:"x_he_error"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &b)
	var markers int
	for _, c := range b.Choices {
		if c.FinishReason == "he_upstream_error" && c.XHeError != nil {
			markers++
		}
	}
	if markers != 1 {
		t.Errorf("failed-leg markers = %d, want 1; body=%s", markers, rr.Body.String())
	}
	if dd.lastTokens != 30 { // only legA (10+20) billed
		t.Errorf("billed tokens = %d, want 30 (OK leg only)", dd.lastTokens)
	}
}

func Test_10_6_INT_013_AB_InvalidModelSet_400(t *testing.T) {
	// Pri: P1 — count≠2 / duplicate → 400 (6.4 validation).
	legA := legHandle("c", "qwen-max", "x", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"qwen-max": legA, "deepseek-v3": legA})
	h := handlers.NewPlaygroundChatHandler(realChat(reg, &countingDeducter{}), ownedResolver(nil), nil)

	for _, hdr := range []string{"qwen-max", "qwen-max,qwen-max", "a,b,c"} {
		rr := doPlayground(h, playgroundBodyJSON("qwen-max"), hdr)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("AB header %q: status = %d, want 400", hdr, rr.Code)
		}
	}
}

func Test_10_6_INT_014_Upstream429_Passthrough(t *testing.T) {
	// Pri: P1 — upstream ResourceExhausted → 429_rate_limit_qps, not swallowed.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": &failingHandle{code: connect.CodeResourceExhausted},
	})
	h := handlers.NewPlaygroundChatHandler(realChat(reg, &countingDeducter{}), ownedResolver(nil), nil)
	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "429_rate_limit_qps" {
		t.Errorf("code = %q, want 429_rate_limit_qps", got)
	}
}

func Test_10_6_INT_015_QuotaExhausted_402_Passthrough(t *testing.T) {
	// Pri: P1 — 402 originates at the pre-flight billing gate (never an upstream).
	// The proxy must pass it through unswallowed. Stub the gate-wrapped chat.
	stub := &recordingChat{errStatus: http.StatusPaymentRequired, errCode: "402_balance_insufficient"}
	h := handlers.NewPlaygroundChatHandler(stub, ownedResolver(nil), nil)
	rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "402_balance_insufficient" {
		t.Errorf("code = %q, want 402_balance_insufficient", got)
	}
}

func Test_10_6_INT_016_Upstream5xxTimeout_502_504(t *testing.T) {
	// Pri: P1 — upstream Unavailable → 502; DeadlineExceeded → 504. Both pass through.
	cases := []struct {
		code connect.Code
		want int
	}{
		{connect.CodeUnavailable, http.StatusBadGateway},
		{connect.CodeDeadlineExceeded, http.StatusGatewayTimeout},
	}
	for _, tc := range cases {
		reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
			"qwen-max": &failingHandle{code: tc.code},
		})
		h := handlers.NewPlaygroundChatHandler(realChat(reg, &countingDeducter{}), ownedResolver(nil), nil)
		rr := doPlayground(h, playgroundBodyJSON("qwen-max"), "")
		if rr.Code != tc.want {
			t.Errorf("upstream %v: status = %d, want %d; body=%s", tc.code, rr.Code, tc.want, rr.Body.String())
		}
	}
}

func Test_10_6_INT_017_ContentSafety_InOut_Applied(t *testing.T) {
	// Pri: P2 — the proxied path still runs the §9.3 入参 content-safety filter
	// (8.2/8.3). "badword" is a known DefaultLexicon term → 400_content_filter.
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": newSingleChunkHandle(canonicalAdapterChunk()),
	})
	chat := realChat(reg, &countingDeducter{},
		handlers.WithSafetyScanner(contentsafety.NewScanner(safetylexicon.DefaultLexicon)))
	h := handlers.NewPlaygroundChatHandler(chat, ownedResolver(nil), nil)

	body := `{"api_key_id":"` + playgroundKeyID + `","model":"qwen-max","messages":[{"role":"user","content":"please say badword now"}]}`
	rr := doPlayground(h, body, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if got := errCodeOf(t, rr); got != "400_content_filter" {
		t.Errorf("code = %q, want 400_content_filter", got)
	}
}

// ----- AC1 blind spots (gateway) ---------------------------------------------

func Test_10_6_BLIND_DATA_001_DoubleBilling_TPMDeductSum(t *testing.T) {
	// [BLIND-SPOT] DATA | Pri: P0 [SEC] — A/B: TPMDeduct == legA.total + legB.total;
	// single: a single deduct of the leg total. Estimate is never billed (the
	// gateway emits no USD — the deducter sees token counts only).
	t.Run("single", func(t *testing.T) {
		reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
			"qwen-max": newSingleChunkHandle(canonicalAdapterChunk()),
		})
		dd := &countingDeducter{}
		h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)
		_ = doPlayground(h, playgroundBodyJSON("qwen-max"), "")
		if dd.calls != 1 || dd.lastTokens != 13 {
			t.Errorf("single deduct calls=%d tokens=%d, want 1/13", dd.calls, dd.lastTokens)
		}
	})
	t.Run("ab_dual", func(t *testing.T) {
		legA := legHandle("a", "qwen-max", "A", 11, 22)   // 33
		legB := legHandle("b", "deepseek-v3", "B", 5, 9)  // 14
		reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
			"qwen-max": legA, "deepseek-v3": legB,
		})
		dd := &countingDeducter{}
		h := handlers.NewPlaygroundChatHandler(realChat(reg, dd), ownedResolver(nil), nil)
		_ = doPlayground(h, playgroundBodyJSON("qwen-max"), "qwen-max,deepseek-v3")
		if dd.calls != 1 || dd.lastTokens != 47 { // 33 + 14
			t.Errorf("A/B deduct calls=%d tokens=%d, want 1/47 (legA.total+legB.total)", dd.calls, dd.lastTokens)
		}
	})
}

func Test_10_6_BLIND_CONCURRENCY_001_PerUserFence_NoCrossLeak(t *testing.T) {
	// [BLIND-SPOT] CONCURRENCY | Pri: P0 [SEC] — two concurrent requests with two
	// different users' api_key_id each stay fenced to their own JWT sub; the
	// downstream context never cross-leaks. Run with -race.
	const (
		userA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		keyA  = "11111111-1111-4111-8111-111111111111"
		userB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		keyB  = "22222222-2222-4222-8222-222222222222"
	)
	// resolver echoes whatever key it is asked for (ownership trusted for this
	// fence test — the point is the per-request context isolation downstream).
	resolver := handlers.PlaygroundKeyResolver(&echoResolver{})
	// STATELESS chat handler: reads the per-request injected context and writes it
	// to THIS request's response only (no shared mutable state → race-free probe of
	// the production handler's per-request context isolation).
	chat := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, _ := middleware.APIKeyIDFromContext(r.Context())
		u, _ := middleware.BearerUserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"seen_api_key_id":"` + k + `","seen_user_id":"` + u + `"}`))
	})
	h := handlers.NewPlaygroundChatHandler(chat, resolver, nil)

	bodyA := `{"api_key_id":"` + keyA + `","model":"qwen-max","messages":[{"role":"user","content":"a"}]}`
	bodyB := `{"api_key_id":"` + keyB + `","model":"qwen-max","messages":[{"role":"user","content":"b"}]}`

	var wg sync.WaitGroup
	results := make([]string, 2) // each goroutine's response body (per-request, race-free)
	run := func(i int, user, body string) {
		defer wg.Done()
		rr := doPlaygroundAs(h, user, body, "")
		results[i] = rr.Body.String()
	}
	wg.Add(2)
	go run(0, userA, bodyA)
	go run(1, userB, bodyB)
	wg.Wait()

	assertSeen := func(out, wantKey, wantUser string) {
		var seen struct {
			K string `json:"seen_api_key_id"`
			U string `json:"seen_user_id"`
		}
		if err := json.Unmarshal([]byte(out), &seen); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if seen.K != wantKey {
			t.Errorf("downstream api_key_id = %q, want %q (no cross-leak)", seen.K, wantKey)
		}
		if seen.U != wantUser {
			t.Errorf("downstream user_id = %q, want %q (no cross-leak)", seen.U, wantUser)
		}
	}
	assertSeen(results[0], keyA, userA)
	assertSeen(results[1], keyB, userB)
}

// echoResolver returns a policy whose api_key_id echoes the requested id (used by
// the concurrency fence test; the handler injects body.APIKeyID regardless, so the
// echo simply confirms the resolver is consulted per request).
type echoResolver struct{}

func (echoResolver) Resolve(_ context.Context, _, apiKeyID string) (*handlers.ResolvedKeyPolicy, error) {
	return &handlers.ResolvedKeyPolicy{APIKeyID: apiKeyID}, nil
}
