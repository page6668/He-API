// CI-only adapter-fake upstream — tests for handler shape.
//
// Story 4.8 T1.7 — 4.8-INFRA-001 (P0 happy path) + 4.8-INFRA-007 (P0
// determinism). Tests the handler directly via net/http/httptest so we
// don't need to spin up TLS.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newReq(t *testing.T, body any) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, chatCompletionsPath, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) chatResponse {
	t.Helper()
	var resp chatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	return resp
}

// 4.8-INFRA-001 — non-streaming happy-path roundtrip.
func TestHappyPath_NonStreaming(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model:    "deepseek-v3",
		Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeResp(t, w)
	if !strings.HasPrefix(got.ID, "chatcmpl-fake-") {
		t.Fatalf("id = %q; want chatcmpl-fake- prefix", got.ID)
	}
	if got.Object != "chat.completion" {
		t.Fatalf("object = %q", got.Object)
	}
	if got.Model != "deepseek-v3" {
		t.Fatalf("model echo mismatch: %q", got.Model)
	}
	if got.Usage == nil || got.Usage.TotalTokens != got.Usage.PromptTokens+got.Usage.CompletionTokens {
		t.Fatalf("usage additivity violation: %+v", got.Usage)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message == nil {
		t.Fatalf("choices shape: %+v", got.Choices)
	}
	if got.Choices[0].Message.Role != "assistant" {
		t.Fatalf("role = %q", got.Choices[0].Message.Role)
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %v", got.Choices[0].FinishReason)
	}
}

// 4.8-INFRA-007 [BLIND-SPOT DATA-001] — determinism: same request → byte-identical response.
func TestDeterminism_Idempotent_Repeat(t *testing.T) {
	w1 := httptest.NewRecorder()
	handleChat(w1, newReq(t, chatRequest{Model: "deepseek-v3", Messages: []chatMessage{{Role: "user", Content: "Hi"}}}))
	w2 := httptest.NewRecorder()
	handleChat(w2, newReq(t, chatRequest{Model: "deepseek-v3", Messages: []chatMessage{{Role: "user", Content: "Hi"}}}))
	if !bytes.Equal(w1.Body.Bytes(), w2.Body.Bytes()) {
		t.Fatalf("BR-1.10 determinism violation:\nA=%s\nB=%s", w1.Body, w2.Body)
	}
}

// 4.8-INFRA-008 — streaming branch: terminal chunk carries usage triple.
func TestStreaming_TerminalChunkCarriesUsage(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, newReq(t, chatRequest{
		Model: "deepseek-v3", Stream: true,
		Messages: []chatMessage{{Role: "user", Content: "Hi"}},
	}))
	// Parse SSE: split on "\n\n", strip "data: " prefix.
	frames := parseSSE(t, w.Body.Bytes())
	if len(frames) < 3 {
		t.Fatalf("got %d frames, want >=3 (bootstrap + content + terminal)", len(frames))
	}
	if frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[len(frames)-1])
	}
	// Penultimate frame MUST be the usage chunk.
	var usageChunk chatChunk
	if err := json.Unmarshal([]byte(frames[len(frames)-2]), &usageChunk); err != nil {
		t.Fatalf("decode usage chunk: %v; frame=%s", err, frames[len(frames)-2])
	}
	if usageChunk.Usage == nil {
		t.Fatalf("BR-2.4: penultimate frame missing usage: %s", frames[len(frames)-2])
	}
	if usageChunk.Usage.TotalTokens != usageChunk.Usage.PromptTokens+usageChunk.Usage.CompletionTokens {
		t.Fatalf("usage additivity violation: %+v", usageChunk.Usage)
	}
}

// 4.8-INFRA-009 [BLIND-SPOT ERROR-003] — malformed JSON body → HTTP 400 + envelope.
func TestRejectsMalformedJson(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, chatCompletionsPath, strings.NewReader("{not-json"))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleChat(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body not JSON: %s", w.Body)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing: %v", body)
	}
	if errObj["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v", errObj["type"])
	}
}

func TestRejectsNonPost(t *testing.T) {
	w := httptest.NewRecorder()
	handleChat(w, httptest.NewRequest(http.MethodGet, chatCompletionsPath, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

// parseSSE turns an SSE byte stream into a list of frame payloads (the
// `data: <payload>` content with the prefix stripped). Empty payloads + the
// trailing CRLF are dropped.
func parseSSE(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	for _, blk := range bytes.Split(body, []byte("\n\n")) {
		line := bytes.TrimSpace(blk)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data: ")) {
			t.Fatalf("non-data SSE line: %q", line)
		}
		out = append(out, string(line[len("data: "):]))
	}
	if _, err := io.Discard.Write(body); err != nil { // suppress unused import warning
		t.Fatal(err)
	}
	return out
}
