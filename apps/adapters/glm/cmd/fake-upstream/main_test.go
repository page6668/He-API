// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-004 happy + determinism.
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

func TestHappyPath_Glm4(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model: "glm-4", Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "glm-4" {
		t.Fatalf("model echo = %q", got.Model)
	}
}

func TestDeterminism_Glm4(t *testing.T) {
	w1 := httptest.NewRecorder()
	w2 := httptest.NewRecorder()
	handleChat(w1, newReq(t, chatRequest{Model: "glm-4"}))
	handleChat(w2, newReq(t, chatRequest{Model: "glm-4"}))
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatalf("BR-1.10 determinism violation:\nA=%s\nB=%s", w1.Body, w2.Body)
	}
}
