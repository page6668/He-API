// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-003 happy + ContextLengthExceeded trigger.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newReq(t *testing.T, body any) *http.Request {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, chatCompletionsPath, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestHappyPath_Moonshot8k(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model: "moonshot-v1-8k", Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "moonshot-v1-8k" {
		t.Fatalf("model = %q", got.Model)
	}
}

// 4.8-INFRA-003 second part — BR-4.5 trigger surfaces context-length-exceeded shape.
func TestContextLengthExceededTrigger(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model:    "moonshot-v1-8k",
		Messages: []chatMessage{{Role: "user", Content: triggerContextLengthExceeded}},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(strings.ToLower(body), "context length") {
		t.Fatalf("body does NOT carry context-length marker: %s", body)
	}
	if !strings.Contains(body, "invalid_request_error") {
		t.Fatalf("body type != invalid_request_error: %s", body)
	}
}
