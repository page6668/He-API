// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-005 happy + TestEchoesEndpointId for BR-1.11.
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

func TestHappyPath_DoubaoFriendlyId(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{Model: "doubao-pro", Messages: []chatMessage{{Role: "user", Content: "Hi"}}}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
}

// 4.8-INFRA-005 BR-1.11 — fake echoes the Volcengine endpoint id verbatim
// so the adapter's per-request back-translate is exercised end-to-end.
func TestEchoesEndpointId(t *testing.T) {
	endpointID := "ep-20240601-abc123"
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{Model: endpointID}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != endpointID {
		t.Fatalf("BR-1.11: fake-upstream MUST echo endpoint id verbatim; got %q want %q", got.Model, endpointID)
	}
	if !strings.HasPrefix(got.Model, "ep-") {
		t.Fatalf("echo lost ep- prefix: %q", got.Model)
	}
}
