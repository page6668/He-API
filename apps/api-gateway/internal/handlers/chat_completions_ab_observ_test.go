// Story 6.4 AC4 — A/B observability + invariants (BR4-1 non-PII slog, BR4-3 one
// QPS tick by construction).
//
// Scenario trace -> docs/qa/assessments/6.4-test-design-20260603.md:
//
//	6.4-UNIT-041/042  non-PII slog chat_completions_ab {ab_models,served,failed,he_request_id}
//	6.4-UNIT-044      one A/B request = one rate-limit tick (fan-out is inside the handler)
package handlers_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
)

// 6.4-UNIT-041/042 (P1) — the chat_completions_ab slog line is emitted with the
// non-PII fields and NEVER leaks message content / user_id (BR4-1).
func TestAB_Slog_NonPII(t *testing.T) {
	var buf bytes.Buffer
	legA := legHandle("cmpl-a", "qwen-max", "secret-A-content", 1, 1)
	legB := legHandle("cmpl-b", "deepseek-v3", "secret-B-content", 1, 1)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	h := handlers.NewChatCompletionsHandler(
		bufLogger(&buf),
		handlers.WithRouter(routingclient.NewDecider(nil, bufLogger(&buf))),
		handlers.WithAdapterRegistry(reg),
	)

	body := `{"model":"qwen-max","messages":[{"role":"user","content":"my-private-prompt"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(routingclient.ABModelsHeader, "qwen-max,deepseek-v3")
	req = req.WithContext(withBearerCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	logs := buf.String()
	if !strings.Contains(logs, "chat_completions_ab") {
		t.Fatalf("missing chat_completions_ab slog line; logs=%s", logs)
	}
	// non-PII fields present.
	for _, want := range []string{"ab_models", "served_models", "qwen-max", "deepseek-v3"} {
		if !strings.Contains(logs, want) {
			t.Errorf("slog missing %q; logs=%s", want, logs)
		}
	}
	// PII MUST NOT leak.
	for _, leak := range []string{"my-private-prompt", "secret-A-content", "secret-B-content"} {
		if strings.Contains(logs, leak) {
			t.Errorf("slog leaked PII %q; logs=%s", leak, logs)
		}
	}
}

// 6.4-UNIT-044 (P1) — one A/B request consumes ONE rate-limit (QPS/RPM) tick by
// construction (Q-J): the A/B fan-out lives INSIDE the handler, downstream of
// the Story-5.3 rate-limit middleware, and the handler invokes NO QPS counter
// (the only billing it does is the single TPMDeduct of summed tokens). This
// asserts the handler made exactly the 2 upstream calls + one token deduction
// for one inbound request (no per-leg request multiplication that a QPS counter
// would see).
func TestAB_OneRequest_OneTokenDeduction(t *testing.T) {
	legA := legHandle("cmpl-a", "qwen-max", "A", 5, 5)
	legB := legHandle("cmpl-b", "deepseek-v3", "B", 5, 5)
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{
		"qwen-max": legA, "deepseek-v3": legB,
	})
	dd := &countingDeducter{}
	h := abHandler(t, reg, dd)

	rr := doABRequest(t, h, "qwen-max,deepseek-v3", false, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// ONE inbound request -> exactly 2 upstream calls (both legs) + ONE token
	// deduction (TPM reflects both legs; QPS is one tick upstream — Q-J).
	if legA.called != 1 || legB.called != 1 {
		t.Errorf("upstream calls A=%d B=%d, want 1/1", legA.called, legB.called)
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want 1 (TPM once with summed legs; not per-leg)", dd.calls)
	}
}
