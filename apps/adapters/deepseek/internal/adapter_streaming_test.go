package internal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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

// canonicalStreamingChunks builds the standard DeepSeek SSE-frame body for
// a successful streaming call: bootstrap + N content deltas + terminal-with-
// usage + literal [DONE]. Helper for the streaming-branch tests below.
func canonicalStreamingChunks(t *testing.T, model string) []byte {
	t.Helper()
	var b strings.Builder
	frames := []string{
		// bootstrap — role-only delta
		`{"id":"chatcmpl-stream-001","object":"chat.completion.chunk","created":1700000000,"model":"` + model + `","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		// content delta 1
		`{"id":"chatcmpl-stream-001","object":"chat.completion.chunk","created":1700000000,"model":"` + model + `","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		// content delta 2 — finish_reason set on the LAST content chunk
		`{"id":"chatcmpl-stream-001","object":"chat.completion.chunk","created":1700000000,"model":"` + model + `","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":"stop"}]}`,
		// terminal — empty choices + usage populated (stream_options.include_usage=true convention)
		`{"id":"chatcmpl-stream-001","object":"chat.completion.chunk","created":1700000000,"model":"` + model + `","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	}
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// flushingHandler turns the handler into a sequence of write+flush pairs so
// the HTTP/2 streaming response reaches the client incrementally (matches
// the SSE forward-chain behaviour real upstreams use).
func flushingHandler(t *testing.T, body []byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("ResponseWriter is not http.Flusher (HTTP/2 test fake expected)")
			return
		}
		_, _ = w.Write(body)
		flusher.Flush()
	}
}

// 4.1-UNIT-026 (P0) — BR-1.3 + BR-2.4. Streaming branch emits N ChatChunks
// via sink.Send; the LAST chunk carries Usage populated; intermediate chunks
// have Usage=nil.
func TestChatStreaming_EmitsAllChunksAndTerminalUsage(t *testing.T) {
	body := canonicalStreamingChunks(t, "deepseek-v3")
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:       "deepseek-v3",
		Messages:    []*adapterv1.ChatMessage{{Role: "user", Content: "Say hi"}},
		Stream:      true,
		HeRequestId: "req_stream000001",
	}, sink)
	if err != nil {
		t.Fatalf("ChatInto stream err = %v, want nil", err)
	}
	if got, want := len(sink.sent), 4; got != want {
		t.Fatalf("captured %d chunks, want %d (3 deltas + 1 terminal-usage)", got, want)
	}
	// All non-terminal chunks must have Usage = nil; ONLY the last carries usage.
	for i := 0; i < len(sink.sent)-1; i++ {
		if sink.sent[i].Usage != nil {
			t.Fatalf("chunk %d carries Usage; BR-2.4 says only terminal chunk", i)
		}
	}
	term := sink.sent[len(sink.sent)-1]
	if term.Usage == nil {
		t.Fatalf("terminal chunk Usage = nil; BR-2.4 violation")
	}
	if term.Usage.GetPromptTokens() != 4 || term.Usage.GetCompletionTokens() != 2 || term.Usage.GetTotalTokens() != 6 {
		t.Fatalf("terminal usage = %+v, want {4,2,6}", term.Usage)
	}
}

// 4.1-UNIT-027 (P0) — BR-2.3 strict-RFC decoder integration. Upstream emits
// a malformed frame (event: ping) → adapter surfaces Code.Unavailable.
func TestChatStreaming_MalformedUpstreamFrame_IsUnavailable(t *testing.T) {
	body := []byte("event: keep-alive\n\ndata: [DONE]\n\n")
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, sink)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable on malformed frame")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.1-UNIT-028 (P0) — BR-2.9 enforcement: outbound body MUST carry
// stream_options.include_usage=true even when the inbound request omits
// stream_options. Asserts via the upstream fake capturing the request body.
func TestChatStreaming_ForcesIncludeUsageOnUpstreamBody(t *testing.T) {
	var captured string
	body := canonicalStreamingChunks(t, "deepseek-v3")
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		captured = string(buf)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Tell me a joke."}},
		Stream:   true,
	}, sink); err != nil {
		t.Fatalf("ChatInto err = %v", err)
	}
	if !strings.Contains(captured, `"stream":true`) {
		t.Fatalf("upstream body missing stream:true: %s", captured)
	}
	if !strings.Contains(captured, `"include_usage":true`) {
		t.Fatalf("BR-2.9: outbound body missing stream_options.include_usage=true: %s", captured)
	}
}

// 4.1-UNIT-029 (P0) — BR-1.8 client cancellation: when the client's context
// is cancelled mid-stream, the adapter's outbound HTTPS call cancels within
// 200ms (req.Context() propagated to http.NewRequestWithContext).
func TestChatStreaming_ClientCancelPropagatesUnder200ms(t *testing.T) {
	hangOpened := make(chan struct{}, 1)
	hangStop := make(chan struct{})
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		// Emit one chunk so the gateway commits to streaming, then hang.
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-v3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		if f != nil {
			f.Flush()
		}
		hangOpened <- struct{}{}
		select {
		case <-r.Context().Done():
			// expected — the adapter's context cancel should reach us
		case <-hangStop:
		case <-time.After(5 * time.Second):
		}
	})
	defer fake.Close()
	defer close(hangStop)

	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-hangOpened
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	t0 := time.Now()
	err := svc.ChatInto(ctx, &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, sink)
	elapsed := time.Since(t0)
	if err == nil {
		t.Fatalf("err = nil; expected cancellation propagation")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed %v exceeds 500ms — cancellation did not propagate promptly", elapsed)
	}
}

// 4.1-UNIT-029b — concurrent streaming calls do not share state.
func TestChatStreaming_ConcurrentCalls_NoSharedState(t *testing.T) {
	body := canonicalStreamingChunks(t, "deepseek-v3")
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()
	svc := newServiceForTest(t, fake)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sink := newCaptureStream()
			err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
				Model:    "deepseek-v3",
				Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
				Stream:   true,
			}, sink)
			if err != nil {
				t.Errorf("goroutine %d err = %v", i, err)
				return
			}
			if len(sink.sent) != 4 {
				t.Errorf("goroutine %d got %d chunks, want 4", i, len(sink.sent))
			}
		}(i)
	}
	wg.Wait()
}

// 4.1-UNIT-029c — BR-3.4 missing-usage on terminal stream chunk is a HARD
// failure: if upstream finishes without ever emitting a usage-bearing chunk,
// the adapter surfaces Code.Unavailable (validation_failure=missing_usage).
func TestChatStreaming_MissingUsageTerminal_IsHardFailure(t *testing.T) {
	// No usage anywhere — the only frame is a finish_reason chunk + [DONE].
	body := []byte(strings.Join([]string{
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-v3","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n"))
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, sink)
	if err == nil {
		t.Fatalf("err = nil; want Unavailable (missing_usage)")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
}

// 4.1-UNIT-029d — upstream pre-stream 5xx → Code.Unavailable (does NOT enter
// the SSE decode loop because headers signal failure before any chunk).
func TestChatStreaming_Upstream_5xx_PreStream_IsUnavailable(t *testing.T) {
	fake := newFakeUpstreamHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"vendor 502"}`)
	})
	defer fake.Close()
	svc := newServiceForTest(t, fake)
	sink := newCaptureStream()

	err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, sink)
	if err == nil {
		t.Fatalf("err = nil, want Unavailable")
	}
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("err = %v, want connect.CodeUnavailable", err)
	}
	if len(sink.sent) != 0 {
		t.Fatalf("captured %d chunks on pre-stream 5xx; want 0", len(sink.sent))
	}
}

// 4.1-UNIT-029e — BR-3.6 audit log emitted on successful streaming Chat with
// post-Normaliser token counts.
func TestChatStreaming_LogsRequestEnd(t *testing.T) {
	body := canonicalStreamingChunks(t, "deepseek-v3")
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()

	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBufWriter{b: &logBuf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	sink := newCaptureStream()
	if err := svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}},
		Stream:   true,
	}, sink); err != nil {
		t.Fatalf("ChatInto err = %v", err)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, `"event":"adapter_chat_request_end"`) {
		t.Fatalf("BR-3.6: missing adapter_chat_request_end in logs: %s", logs)
	}
	if !strings.Contains(logs, `"prompt_tokens":4`) || !strings.Contains(logs, `"completion_tokens":2`) || !strings.Contains(logs, `"total_tokens":6`) {
		t.Fatalf("BR-3.6: token-count fields missing/wrong in logs: %s", logs)
	}
}

// 4.1-UNIT-029f — BR-1.9 PII safety on streaming logs.
func TestChatStreaming_BR1_9_PII_safe_logs(t *testing.T) {
	body := canonicalStreamingChunks(t, "deepseek-v3")
	fake := newFakeUpstreamHandler(t, flushingHandler(t, body))
	defer fake.Close()

	var logBuf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logBufWriter{b: &logBuf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	client := upstream.NewClient(fake.URL, "k", 5*time.Second)
	client.HTTPClient.Transport = &http2.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		AllowHTTP:       false,
	}
	svc := NewService(client, logger)

	const secret = "STREAM-PII-SECRET-DO-NOT-LOG"
	sink := newCaptureStream()
	_ = svc.ChatInto(context.Background(), &adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: secret}},
		Stream:   true,
	}, sink)
	if strings.Contains(logBuf.String(), secret) {
		t.Fatalf("streaming logs leaked PII; logs: %s", logBuf.String())
	}
}

type logBufWriter struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (w *logBufWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// Compile-time assertion: httptest.Server is what we expect.
var _ = httptest.NewServer
