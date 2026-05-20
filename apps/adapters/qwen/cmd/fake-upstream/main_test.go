// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-002 happy-path roundtrip + determinism.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newReq(t *testing.T, body any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, chatCompletionsPath, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// 4.8-INFRA-002 — DashScope OpenAI-compat happy path.
func TestHappyPath_NonStreaming_QwenMax(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model: "qwen-max", Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "qwen-max" {
		t.Fatalf("model = %q", got.Model)
	}
	if got.Usage.TotalTokens != got.Usage.PromptTokens+got.Usage.CompletionTokens {
		t.Fatalf("usage additivity: %+v", got.Usage)
	}
}

// 4.8-INFRA-007 — determinism.
func TestDeterminism_QwenPlus(t *testing.T) {
	w1 := httptest.NewRecorder()
	w2 := httptest.NewRecorder()
	handleChat(w1, newReq(t, chatRequest{Model: "qwen-plus"}))
	handleChat(w2, newReq(t, chatRequest{Model: "qwen-plus"}))
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatalf("BR-1.10 determinism violation:\nA=%s\nB=%s", w1.Body, w2.Body)
	}
}
