// Story 4.1 Phase B — Integration scenarios for the streaming branch of
// the DeepSeek adapter (4.1-INT-007..014). Each scenario wires the adapter
// to an HTTP/2-enabled TLS upstream fake emitting OpenAI-spec SSE frames
// and asserts the Connect-RPC server-streaming response shape end-to-end.
package tests

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/he-api/he-api/apps/adapters/deepseek/internal/upstream"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// streamingUpstreamBody builds the SSE frame sequence DeepSeek would emit
// for a happy-path stream=true call: bootstrap + 2 content deltas + tail-
// usage + literal [DONE].
func streamingUpstreamBody() []byte {
	frames := []string{
		`{"id":"chatcmpl-stream-int","object":"chat.completion.chunk","created":1700000000,"model":"deepseek-v3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-stream-int","object":"chat.completion.chunk","created":1700000000,"model":"deepseek-v3","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-stream-int","object":"chat.completion.chunk","created":1700000000,"model":"deepseek-v3","choices":[{"index":0,"delta":{"content":" there"},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-stream-int","object":"chat.completion.chunk","created":1700000000,"model":"deepseek-v3","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
	}
	var b strings.Builder
	for _, f := range frames {
		b.WriteString("data: ")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

func writeFlushed(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// 4.1-INT-007 (P0) — Happy-path streaming end-to-end. Adapter consumes SSE
// from upstream, emits one ChatChunk per frame to the Connect-RPC client.
func TestINT_007_Streaming_HappyPath(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		writeFlushed(w, streamingUpstreamBody())
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model:    "deepseek-v3",
		Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "Say hi"}},
		Stream:   true,
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	var chunks []*adapterv1.ChatChunk
	for resp.Receive() {
		chunks = append(chunks, resp.Msg())
	}
	if e := resp.Err(); e != nil {
		t.Fatalf("stream Err = %v", e)
	}
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4 (3 deltas + 1 terminal-usage)", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if last.Usage == nil || last.Usage.GetTotalTokens() != 6 {
		t.Fatalf("terminal chunk usage = %v, want total=6", last.Usage)
	}
}

// 4.1-INT-008 (P0) — Streaming + Accept header: outbound HTTPS request
// carries `Accept: text/event-stream`.
func TestINT_008_Streaming_AcceptHeader(t *testing.T) {
	var seenAccept string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenAccept = r.Header.Get("Accept")
		writeFlushed(w, streamingUpstreamBody())
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	for resp.Receive() {
	}
	if seenAccept != "text/event-stream" {
		t.Fatalf("upstream Accept = %q, want text/event-stream", seenAccept)
	}
}

// 4.1-INT-009 (P0) — BR-2.9 forced include_usage. Even when the inbound
// ChatRequest omits stream_options, the outbound body must carry
// stream_options.include_usage=true.
func TestINT_009_Streaming_Forces_IncludeUsage(t *testing.T) {
	var capturedBody string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		capturedBody = string(buf)
		writeFlushed(w, streamingUpstreamBody())
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, err := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	for resp.Receive() {
	}
	if !strings.Contains(capturedBody, `"stream":true`) {
		t.Fatalf("upstream body missing stream:true: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, `"include_usage":true`) {
		t.Fatalf("BR-2.9: outbound body missing stream_options.include_usage=true: %s", capturedBody)
	}
}

// 4.1-INT-010 (P0) — BR-2.4 terminal chunk carries usage; intermediate
// chunks do not.
func TestINT_010_Streaming_TerminalUsageOnLastChunk(t *testing.T) {
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		writeFlushed(w, streamingUpstreamBody())
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	var chunks []*adapterv1.ChatChunk
	for resp.Receive() {
		chunks = append(chunks, resp.Msg())
	}
	for i := 0; i < len(chunks)-1; i++ {
		if chunks[i].Usage != nil {
			t.Fatalf("chunk %d carries Usage; BR-2.4 says only terminal", i)
		}
	}
	if chunks[len(chunks)-1].Usage == nil {
		t.Fatalf("terminal chunk missing Usage")
	}
}

// 4.1-INT-011 (P0) — BR-1.5 request-id propagated as Connect-RPC header
// AND threaded onto the upstream X-Request-Id, ON the streaming path.
func TestINT_011_Streaming_RequestId_Propagation(t *testing.T) {
	var seenReqID string
	fake := startFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		seenReqID = r.Header.Get("X-Request-Id")
		writeFlushed(w, streamingUpstreamBody())
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()

	client := newAdapterClient(adapter.URL)
	req := connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	})
	req.Header().Set("X-He-Request-Id", "req_stream_intgrt")
	resp, err := client.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat err = %v", err)
	}
	for resp.Receive() {
	}
	if seenReqID != "req_stream_intgrt" {
		t.Fatalf("upstream X-Request-Id = %q, want req_stream_intgrt", seenReqID)
	}
}

// 4.1-INT-014 (P0) — BR-3.4 missing usage on terminal stream chunk → adapter
// surfaces Connect-RPC CodeUnavailable.
func TestINT_014_Streaming_MissingUsage_HardFailure(t *testing.T) {
	body := []byte(strings.Join([]string{
		`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-v3","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n"))
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		writeFlushed(w, body)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	// Consume any chunks; the failure surfaces via Err() at EOS.
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err() = nil; want CodeUnavailable on missing usage")
	} else {
		var ce *connect.Error
		if !errors.As(e, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("Err = %v; want CodeUnavailable", e)
		}
	}
}

// 4.1-INT-014b — Strict-RFC decoder integration: a malformed SSE frame from
// upstream surfaces as CodeUnavailable (no silent skip).
func TestINT_014b_Streaming_MalformedFrame_NoSilentSkip(t *testing.T) {
	body := []byte("event: ping\n\ndata: [DONE]\n\n")
	fake := startFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		writeFlushed(w, body)
	})
	defer fake.Close()
	adapter := startAdapterServer(t, fake.URL)
	defer adapter.Close()
	client := newAdapterClient(adapter.URL)
	resp, _ := client.Chat(context.Background(), connect.NewRequest(&adapterv1.ChatRequest{
		Model: "deepseek-v3", Messages: []*adapterv1.ChatMessage{{Role: "user", Content: "x"}}, Stream: true,
	}))
	for resp.Receive() {
	}
	if e := resp.Err(); e == nil {
		t.Fatalf("Err() = nil; want CodeUnavailable on malformed frame")
	}
}

// Compile-time anchor.
var _ = upstream.NewClient
