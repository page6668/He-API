// Story 9.6 (T2.4, BR-1.6) — the do-not-regress Chat:true fence on
// /v1/chat/completions. Because the catalogue now contains a NON-chat model
// (doubao-asr), the chat handler MUST reject a KNOWN non-chat id pre-dispatch
// while leaving every existing Chat:true id byte-identically routable.
//
// 9.6-INT-002 (negative): doubao-asr on /v1/chat/completions → 400.
// 9.6-INT-003 (positive regression): every Chat:true catalogue id still passes
// the fence (reaches the mock/dispatch path).
package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postChat(h *ChatCompletionsHandler, model string) *httptest.ResponseRecorder {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(asrAuthedCtx(req.Context()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// 9.6-INT-002 — a Transcription-only id (doubao-asr) on the chat endpoint → 400
// "does not support chat completions" pre-dispatch.
func TestChatFence_ASRModelRejected(t *testing.T) {
	h := NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	rr := postChat(h, "doubao-asr")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if !strings.Contains(env.Error.Message, "does not support chat completions") {
		t.Fatalf("message=%q", env.Error.Message)
	}
}

// 9.6-INT-003 — every existing Chat:true catalogue id passes the fence (not
// rejected with the chat-capability message). Iterating the SHARED capability
// map guarantees no Chat:true id silently regresses.
func TestChatFence_AllChatModelsPass(t *testing.T) {
	h := NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for id, caps := range capabilitiesByModelID {
		if !caps.Chat {
			continue // non-chat ids (doubao-asr) are covered by the negative test
		}
		rr := postChat(h, id)
		// The fence must NOT fire: a Chat:true model reaches the mock dispatch
		// path (200) — never the chat-capability 400.
		if rr.Code == http.StatusBadRequest && strings.Contains(rr.Body.String(), "does not support chat completions") {
			t.Fatalf("REGRESSION: Chat:true id %q was fenced off the chat endpoint", id)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("Chat:true id %q: status=%d body=%s", id, rr.Code, rr.Body.String())
		}
	}
}

// 9.6-INT-003 — an UNKNOWN model id is NOT fenced (the comma-ok guard fences
// only KNOWN non-chat ids; pre-9.6 unknown-model behaviour is preserved).
func TestChatFence_UnknownModelNotFenced(t *testing.T) {
	h := NewChatCompletionsHandler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	rr := postChat(h, "some-unlisted-model")
	if rr.Code == http.StatusBadRequest && strings.Contains(rr.Body.String(), "does not support chat completions") {
		t.Fatalf("unknown model must NOT be fenced (byte-identical pre-9.6): %s", rr.Body.String())
	}
}
