package internal

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	"golang.org/x/net/http2"
)

// canonicalDeepSeekResponse is the JSON shape DeepSeek returns on the
// non-streaming happy path. Test fakes echo this verbatim so the adapter
// exercises the same decode path as production.
func canonicalDeepSeekResponse(content string) []byte {
	stop := "stop"
	return mustJSON(upstream.ChatResponseJSON{
		ID:      "chatcmpl-fake-001",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "deepseek-v3",
		Choices: []upstream.ChatChoiceJSON{{
			Index:        0,
			Message:      &upstream.ChatMessage{Role: "assistant", Content: content},
			FinishReason: &stop,
		}},
		Usage: &upstream.RawUsage{PromptTokens: 5, CompletionTokens: 8, TotalTokens: 13},
	})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// 4.1-UNIT-007 (P0) — BR-1.3 non-streaming branch emits ONE ChatChunk with
// usage + finish_reason populated.
func TestChat_NonStreaming_SingleTerminalChunk(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalDeepSeekResponse("Hello back"))
	defer fake.Close()

	svc := newServiceForTest(t, fake)
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:        "deepseek-v3",
		Messages:     []*adapterv1.ChatMessage{{Role: "user", Content: "Hi"}},
		Stream:       false,
		HeRequestId:  "req_111111111111",
	}, stream)
	if err != nil {
		t.Fatalf("Chat err = %v, want nil", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d chunks, want 1", len(stream.sent))
	}
	c := stream.sent[0]
	if c.Object != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", c.Object)
	}
	if c.Usage == nil || c.Usage.GetPromptTokens() != 5 || c.Usage.GetCompletionTokens() != 8 || c.Usage.GetTotalTokens() != 13 {
		t.Fatalf("usage = %#v, want {5,8,13}", c.Usage)
	}
	if c.GetFinishReason() != "stop" {
		t.Fatalf("finish_reason = %q, want stop", c.GetFinishReason())
	}
	if len(c.Choices) != 1 || c.Choices[0].Delta == nil || c.Choices[0].Delta.GetContent() != "Hello back" {
		t.Fatalf("choices = %#v", c.Choices)
	}
}

// 4.1-UNIT-008 (P0) — BR-1.3 + R7. 50 concurrent Chat calls produce 50
// independent ChatChunks; no state shared across calls. Run with `go test -race`.
func TestChat_NonStreaming_ConcurrentCalls_NoSharedState(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalDeepSeekResponse("ok"))
	defer fake.Close()
	svc := newServiceForTest(t, fake)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stream := newCaptureStream()
			if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    "deepseek-v3",
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
			}, stream); err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			if len(stream.sent) != 1 {
				t.Errorf("goroutine %d: %d chunks", i, len(stream.sent))
			}
		}(i)
	}
	wg.Wait()
}

// 4.1-UNIT-009 (P0) — BR-1.8 context-deadline propagated to upstream. When
// the inbound context carries a tight deadline, the outbound HTTP request
// cancels promptly. We verify via a fake that hangs indefinitely and assert
// the call returns a deadline-exceeded Connect-RPC error inside the timeout
// budget.
func TestChat_NonStreaming_ContextDeadlinePropagates(t *testing.T) {
	hang := make(chan struct{})
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		<-hang
	})
	defer fake.Close()
	defer close(hang)
	svc := newServiceForTest(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	stream := newCaptureStream()
	err := svc.ChatInto(ctx, &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("err = nil, want deadline-related error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed %v exceeds 2s — deadline not propagated", elapsed)
	}
	// Connect-RPC code should be DeadlineExceeded.
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		if connectErr.Code() != connect.CodeDeadlineExceeded {
			t.Fatalf("connect code = %s, want DeadlineExceeded", connectErr.Code())
		}
	}
}

// 4.1-UNIT-010 (P0) — BR-1.9 PII-safe logs. The adapter's slog records
// must NOT contain the prompt content. Use a slog test-double to capture
// records and assert.
func TestChat_NonStreaming_BR1_9_PII_safe_logs(t *testing.T) {
	fake := newFakeUpstream(t, http.StatusOK, canonicalDeepSeekResponse("ok"))
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	stream := newCaptureStream()
	const secret = "PII-SECRET-PHRASE-DO-NOT-LOG"
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: secret}},
	}, stream); err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("slog logs leaked PII; logs: %s", buf.String())
	}
	// Positive — required attribute keys MUST be present.
	for _, key := range []string{`"event"`, `"model"`, `"messages_count"`, `"prompt_tokens"`} {
		if !strings.Contains(buf.String(), key) {
			t.Fatalf("missing log key %s in %s", key, buf.String())
		}
	}
}

// 4.1-UNIT-014 / -015 (P0) — BR-1.4 mapping. Upstream 5xx → Connect-RPC
// Code.Unavailable; upstream 401 → Code.Unavailable (auth_revoked kind);
// timeout → Code.DeadlineExceeded (covered above).
func TestChat_NonStreaming_Upstream_5xx_MapsToUnavailable(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"vendor 502"}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("err is not *connect.Error: %T", err)
	}
	if connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("code = %s, want Unavailable", connectErr.Code())
	}
}

func TestChat_NonStreaming_Upstream_401_MapsToUnavailable_AuthRevoked(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid_api_key"}}`)
	})
	defer fake.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
	// M2 — slog must carry upstream_error_kind=auth_revoked.
	if !strings.Contains(buf.String(), `"upstream_error_kind":"auth_revoked"`) {
		t.Fatalf("M2: slog missing upstream_error_kind=auth_revoked; logs: %s", buf.String())
	}
}

// Missing-usage hostile path (BR-3.4): upstream returns 200 with no `usage`
// block → adapter surfaces Code.Unavailable + validation_failure=missing_usage.
func TestChat_NonStreaming_MissingUsage_IsHardFailure(t *testing.T) {
	stop := "stop"
	body := mustJSON(upstream.ChatResponseJSON{
		ID: "chatcmpl-x", Object: "chat.completion", Created: 1, Model: "deepseek-v3",
		Choices: []upstream.ChatChoiceJSON{{Index: 0, Message: &upstream.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: &stop}},
		// usage intentionally omitted
	})
	fake := newFakeUpstream(t, http.StatusOK, body)
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	stream := newCaptureStream()
	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
	}, stream)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable (missing_usage)")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// --- helpers --------------------------------------------------------------

type captureStream struct {
	sent []*adapterv1.ChatChunk
}

func newCaptureStream() *captureStream { return &captureStream{} }

// Send is the ServerStream surface the adapter Chat method calls. The
// real connect.ServerStream is HTTP-bound; captureStream implements the
// trimmed-down interface we need for unit-test capture.
func (c *captureStream) Send(chunk *adapterv1.ChatChunk) error {
	c.sent = append(c.sent, chunk)
	return nil
}

// newFakeUpstream constructs a TLS httptest server returning status + body
// for any POST. The TLS-ness exercises the HTTP/2-forced transport's TLS
// handshake path.
func newFakeUpstream(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	return newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

// newFakeUpstreamHandler wires an arbitrary handler into an HTTP/2-enabled
// TLS httptest server. EnableHTTP2 must be set BEFORE StartTLS so the TLS
// config's NextProtos includes "h2"; httptest.NewTLSServer (the bare form)
// does NOT do this and would surface as `tls: no application protocol` on
// the HTTP/2-forced client side.
func newFakeUpstreamHandler(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(h))
	s.EnableHTTP2 = true
	s.StartTLS()
	return s
}

func newServiceForTest(t *testing.T, fake *httptest.Server) *Service {
	t.Helper()
	client := upstream.NewClient(fake.URL, "test-api-key", 5*time.Second)
	// Skip TLS verification in tests — the httptest server uses a self-signed cert.
	// The HTTP/2 transport keeps wire-protocol parity with prod (OQ7).
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return NewService(client, logger)
}
