// Go port of apps/api-gateway/tests/_protocol_invariants.py — the shared
// OpenAI-protocol shape-assertion library (Story 4.8). There is no Go-native
// drop-in oracle in the repo, so this file builds the equivalent for the Go SDK
// (Dev Notes: "Go 侧须新建等价的形状断言 helper"). It also holds the hermetic
// transport plumbing (a capturing http.RoundTripper + SSE httptest helpers) that
// every drop-in test shares — R-OQ-10.4-3: hermetic = option.WithHTTPClient(stub
// RoundTripper); SSE via httptest.Server; NO real gateway.
package heapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// REQUEST_ID_RE mirrors _protocol_invariants.py REQUEST_ID_RE (rest-api-spec §5.1.2).
var REQUEST_ID_RE = regexp.MustCompile(`^req_[0-9a-f]{12}$`)

var canonicalFinishReasons = map[string]bool{
	"stop": true, "length": true, "tool_calls": true, "content_filter": true,
}

const clockSkewSeconds = int64(60)

func nowWithSkew() int64 { return time.Now().Unix() + clockSkewSeconds }

// ---------------------------------------------------------------------------
// Hermetic transport: a thread-safe capturing RoundTripper (CONCURRENCY-001
// shares one client across goroutines, so capture must be race-free).
// ---------------------------------------------------------------------------

type capturedRequest struct {
	Method string
	URL    string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

type stubTransport struct {
	mu       sync.Mutex
	captured []capturedRequest
	respond  func(req *http.Request) (*http.Response, error)
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cr := capturedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Path:   req.URL.Path,
		Query:  req.URL.RawQuery,
		Header: req.Header.Clone(),
	}
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		cr.Body = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	s.mu.Lock()
	s.captured = append(s.captured, cr)
	s.mu.Unlock()
	return s.respond(req)
}

func (s *stubTransport) last(t *testing.T) capturedRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.captured) == 0 {
		t.Fatalf("no request was captured by the stub transport")
	}
	return s.captured[len(s.captured)-1]
}

func (s *stubTransport) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.captured)
}

// jsonResponder returns a respond func emitting a single JSON response.
func jsonResponder(status int, body string, headers map[string]string) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		h := http.Header{"Content-Type": []string{"application/json"}}
		for k, v := range headers {
			h.Set(k, v)
		}
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
}

// newStubClient builds a heapi client whose transport is the given stub. Retries
// are disabled so error-path assertions see exactly one outgoing attempt.
func newStubClient(t *testing.T, stub *stubTransport, opts ...option.RequestOption) openai.Client {
	t.Helper()
	base := []option.RequestOption{
		option.WithHTTPClient(&http.Client{Transport: stub}),
		option.WithMaxRetries(0),
	}
	return NewClient(append(base, opts...)...)
}

// ---------------------------------------------------------------------------
// SSE httptest helper (streaming tests — R-OQ-10.4-3 "SSE via httptest.Server").
// ---------------------------------------------------------------------------

// sseServer starts an httptest.Server that streams the given raw SSE frames
// (each already terminated by a blank line) then closes. Returns the server.
func sseServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sseData(json string) string { return "data: " + json + "\n\n" }

const sseDone = "data: [DONE]\n\n"

// ---------------------------------------------------------------------------
// Shape assertions (1:1 with the five Python helpers).
// ---------------------------------------------------------------------------

func assertUsageTriple(t *testing.T, prompt, completion, total int64, allowZeroCompletion bool) {
	t.Helper()
	if prompt <= 0 {
		t.Errorf("usage.prompt_tokens MUST be positive; got %d", prompt)
	}
	if allowZeroCompletion {
		if completion < 0 {
			t.Errorf("usage.completion_tokens MUST be non-negative; got %d", completion)
		}
	} else if completion <= 0 {
		t.Errorf("usage.completion_tokens MUST be positive; got %d", completion)
	}
	if total != prompt+completion {
		t.Errorf("Story-4.1 BR-3.3 invariant: total=%d != prompt=%d + completion=%d", total, prompt, completion)
	}
}

func assertChatCompletionShape(t *testing.T, resp *openai.ChatCompletion, expectedModel string) {
	t.Helper()
	if resp == nil {
		t.Fatalf("BR-2.2 response MUST be non-nil")
	}
	if !strings.HasPrefix(resp.ID, "chatcmpl-") {
		t.Errorf("BR-2.2(a) id MUST start with 'chatcmpl-'; got %q", resp.ID)
	}
	if string(resp.Object) != "chat.completion" {
		t.Errorf("BR-2.2(b) object MUST be 'chat.completion'; got %q", resp.Object)
	}
	if resp.Created <= 0 || resp.Created > nowWithSkew() {
		t.Errorf("BR-2.2(c) created MUST be positive int <= now+60s; got %d", resp.Created)
	}
	if resp.Model != expectedModel {
		t.Errorf("BR-2.2(d) model mismatch: got %q want %q", resp.Model, expectedModel)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("BR-2.2(e) choices MUST be non-empty")
	}
	c0 := resp.Choices[0]
	if c0.Index != 0 {
		t.Errorf("BR-2.2(f) choices[0].index MUST be 0; got %d", c0.Index)
	}
	if c0.Message.Role != "assistant" {
		t.Errorf("BR-2.2(g) message.role MUST be 'assistant'; got %q", c0.Message.Role)
	}
	if len(c0.Message.Content) == 0 {
		t.Errorf("BR-2.2(h) message.content MUST be non-empty")
	}
	if !canonicalFinishReasons[c0.FinishReason] {
		t.Errorf("BR-2.2(i) finish_reason %q not canonical", c0.FinishReason)
	}
	assertUsageTriple(t, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, false)
}

// chunkRole is "bootstrap" | "content" | "terminal".
func assertChatChunkShape(t *testing.T, chunk openai.ChatCompletionChunk, expectedModel, kind string) {
	t.Helper()
	if !strings.HasPrefix(chunk.ID, "chatcmpl-") {
		t.Errorf("BR-2.3(a) chunk.id MUST start with 'chatcmpl-'; got %q", chunk.ID)
	}
	if string(chunk.Object) != "chat.completion.chunk" {
		t.Errorf("BR-2.3(b) chunk.object MUST be 'chat.completion.chunk'; got %q", chunk.Object)
	}
	if chunk.Created <= 0 || chunk.Created > nowWithSkew() {
		t.Errorf("BR-2.3(c) chunk.created out of range; got %d", chunk.Created)
	}
	if chunk.Model != expectedModel {
		t.Errorf("BR-2.3(d) chunk.model mismatch: got %q want %q", chunk.Model, expectedModel)
	}
	if len(chunk.Choices) == 0 {
		t.Fatalf("BR-2.3(e) chunk.choices MUST be non-empty")
	}
	c0 := chunk.Choices[0]
	if c0.Index != 0 {
		t.Errorf("BR-2.3(e) chunk.choices[0].index MUST be 0; got %d", c0.Index)
	}
	switch kind {
	case "bootstrap":
		if c0.Delta.Role != "assistant" {
			t.Errorf("BR-2.3(g) bootstrap delta.role MUST be 'assistant'; got %q", c0.Delta.Role)
		}
	case "terminal":
		if c0.FinishReason != "stop" {
			t.Errorf("BR-2.3(i) terminal finish_reason MUST be 'stop'; got %q", c0.FinishReason)
		}
		if chunk.Usage.TotalTokens != 0 { // usage present on this tail chunk
			assertUsageTriple(t, chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens, chunk.Usage.TotalTokens, true)
		}
	default: // content
		if len(c0.Delta.Content) == 0 {
			t.Errorf("BR-2.3(h) content delta.content MUST be non-empty")
		}
	}
}

func assertEmbeddingShape(t *testing.T, resp *openai.CreateEmbeddingResponse, expectedModel string) {
	t.Helper()
	if resp == nil {
		t.Fatalf("BR-2.4 response MUST be non-nil")
	}
	if string(resp.Object) != "list" {
		t.Errorf("BR-2.4(a) response.object MUST be 'list'; got %q", resp.Object)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("BR-2.4(b) response.data MUST be non-empty")
	}
	dim := -1
	for i, item := range resp.Data {
		if string(item.Object) != "embedding" {
			t.Errorf("BR-2.4(c) data[%d].object MUST be 'embedding'; got %q", i, item.Object)
		}
		if item.Index != int64(i) {
			t.Errorf("BR-2.4(d) data[%d].index MUST be %d; got %d", i, i, item.Index)
		}
		if len(item.Embedding) == 0 {
			t.Errorf("BR-2.4(e) data[%d].embedding MUST be non-empty", i)
		}
		if dim == -1 {
			dim = len(item.Embedding)
		} else if len(item.Embedding) != dim {
			t.Errorf("BR-2.4(e) data[%d].embedding length %d != dim %d", i, len(item.Embedding), dim)
		}
	}
	if resp.Model != expectedModel {
		t.Errorf("BR-2.4(f) response.model mismatch: got %q want %q", resp.Model, expectedModel)
	}
	if resp.Usage.PromptTokens <= 0 {
		t.Errorf("BR-2.4(g) usage.prompt_tokens MUST be positive; got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != resp.Usage.PromptTokens {
		t.Errorf("BR-2.4(g) embeddings invariant: total=%d != prompt=%d", resp.Usage.TotalTokens, resp.Usage.PromptTokens)
	}
}

func assertModelEntryShape(t *testing.T, m openai.Model) {
	t.Helper()
	if len(m.ID) == 0 {
		t.Errorf("BR-2.3b(a) entry.id MUST be non-empty")
	}
	if string(m.Object) != "model" {
		t.Errorf("BR-2.3b(b) entry.object MUST be 'model'; got %q", m.Object)
	}
	if m.Created <= 0 || m.Created > nowWithSkew() {
		t.Errorf("BR-2.3b(c) entry.created out of range; got %d", m.Created)
	}
	if len(m.OwnedBy) == 0 {
		t.Errorf("BR-2.3b(d) entry.owned_by MUST be non-empty")
	}
	// capabilities (Story 4.7) lands in JSON.ExtraFields under the typed
	// openai.Model and is tolerated, not required here (INT-021 note).
}

// assertErrorEnvelopeShape adapts BR-2.5: the typed *openai.Error IS the parsed
// 5-field envelope. Validates code, status, message, canonical type mapping,
// and the He-API he_request_id extension (read via HeRequestID).
func assertErrorEnvelopeShape(t *testing.T, err error, expectedCode string, expectedStatus int) {
	t.Helper()
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("BR-2.5 expected *openai.Error; got %T: %v", err, err)
	}
	if apiErr.Code != expectedCode {
		t.Errorf("BR-2.5(c) error.code mismatch: got %q want %q", apiErr.Code, expectedCode)
	}
	if apiErr.StatusCode != expectedStatus {
		t.Errorf("BR-2.5 status mismatch: got %d want %d", apiErr.StatusCode, expectedStatus)
	}
	if len(apiErr.Message) == 0 {
		t.Errorf("BR-2.5(d) error.message MUST be non-empty")
	}
	switch {
	case expectedStatus >= 400 && expectedStatus < 500:
		if apiErr.Type != "invalid_request_error" {
			t.Errorf("BR-2.5(f) 4xx MUST map to 'invalid_request_error'; got %q", apiErr.Type)
		}
	case expectedStatus >= 500 && expectedStatus < 600:
		if apiErr.Type != "server_error" {
			t.Errorf("BR-2.5(f) 5xx MUST map to 'server_error'; got %q", apiErr.Type)
		}
	}
	if rid := HeRequestID(err); !REQUEST_ID_RE.MatchString(rid) {
		t.Errorf("BR-2.5(h) he_request_id MUST match %s; got %q", REQUEST_ID_RE.String(), rid)
	}
}

// ---------------------------------------------------------------------------
// Canonical mock-envelope builders.
// ---------------------------------------------------------------------------

func mockChatCompletionJSON(model string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-test123","object":"chat.completion","created":%d,"model":%q,`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],`+
		`"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`, time.Now().Unix(), model)
}

func mockEmbeddingJSON(model string) string {
	return fmt.Sprintf(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],`+
		`"model":%q,"usage":{"prompt_tokens":5,"total_tokens":5}}`, model)
}

func mockModelsListJSON() string {
	return fmt.Sprintf(`{"object":"list","data":[{"id":"qwen-max","object":"model","created":%d,"owned_by":"he-api",`+
		`"capabilities":{"chat":true}}]}`, time.Now().Unix())
}

// mockErrorEnvelope returns the canonical 5-field He-API error body.
func mockErrorEnvelope(code, errType, requestID string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":"something went wrong","type":%q,"param":null,"he_request_id":%q}}`,
		code, errType, requestID)
}
