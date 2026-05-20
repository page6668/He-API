// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-006 happy path on Qianfan v2 path.
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

func TestHappyPath_Ernie4(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model: "ernie-4.0", Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "ernie-4.0" {
		t.Fatalf("model echo = %q", got.Model)
	}
}

// Confirms the OQ-4.6-1 v2 path is the registered handler route.
func TestQianfanV2Path(t *testing.T) {
	if chatCompletionsPath != "/v2/chat/completions" {
		t.Fatalf("OQ-4.6-1 path drift: %q", chatCompletionsPath)
	}
}
