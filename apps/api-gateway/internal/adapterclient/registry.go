// Package adapterclient owns the gateway-side model-id → adapter-endpoint
// resolution + Connect-RPC client wrapper.
//
// Architecture (Architect Round 2 OQ4 ruling, Story 4.1):
//
//	Registry.Resolve(modelID) → (handle, ok) is the abstraction boundary.
//	Story 4.1 ships an in-process implementation; Epic 6 routing-svc
//	swaps the implementation WITHOUT touching ChatCompletionsHandler.
//	Stories 4.2-4.6 only register entries in the same Registry — they do
//	not invent a parallel resolution mechanism.
//
// Story 4.1 only registers `deepseek-v3` → DEEPSEEK_ADAPTER_ENDPOINT (env
// var). All other models fall through to the Story-3.3 / 3.4 / 3.5 mock
// path, which retires progressively as Stories 4.2-4.6 land.
package adapterclient

import (
	"net/http"
	"os"
	"sync"

	"connectrpc.com/connect"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
	adapterv1connect "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1/adapterv1connect"
)

// DeepSeekEndpointEnv is the env-var the startup-loader reads for the
// DeepSeek adapter's gRPC endpoint. Stories 4.2-4.6 will add sibling env
// vars (QWEN_ADAPTER_ENDPOINT, KIMI_ADAPTER_ENDPOINT, ...) following the
// same pattern.
const DeepSeekEndpointEnv = "DEEPSEEK_ADAPTER_ENDPOINT"

// DeepSeekModelID is the canonical model identifier the gateway accepts for
// the DeepSeek adapter. The same string lives in the upstream-vendor's
// model-name space + the seed row for he_api.models (Story 4.7).
const DeepSeekModelID = "deepseek-v3"

// ClientHandle is the abstraction Stories 4.2-4.6 + the Story 4.1
// chat-completions handler invoke. The concrete type is a Connect-RPC
// AdapterServiceClient wrapped to surface Chat as a request/stream pair
// the handler can consume without knowing the underlying transport.
type ClientHandle interface {
	Chat(ctx contextLike, req *adapterv1.ChatRequest, headers http.Header) (Stream, error)
}

// contextLike is a forward-declared type alias to context.Context — kept
// here as a small interface so the package's public surface doesn't force
// every caller of Resolve to import context. Callers passing real
// context.Context values satisfy this surface transparently.
type contextLike = ctxAlias

// Stream is the iterator surface a ClientHandle returns. Phase A only uses
// the terminal-chunk path (one Recv + one Close); Phase B extends with the
// streaming-Recv loop.
type Stream interface {
	Receive() bool
	Msg() *adapterv1.ChatChunk
	Err() error
	Close() error
}

// Registry is the model-id → ClientHandle map. Constructed once at startup
// from env vars; reads are concurrent-safe (immutable post-construction).
type Registry struct {
	mu      sync.RWMutex
	handles map[string]ClientHandle
}

// NewRegistry constructs a Registry from a model-id → endpoint map. Empty
// endpoint values are silently skipped (so callers can pass `os.Getenv(...)`
// without pre-filtering).
func NewRegistry(endpoints map[string]string) *Registry {
	r := &Registry{handles: make(map[string]ClientHandle, len(endpoints))}
	for modelID, endpoint := range endpoints {
		if endpoint == "" {
			continue
		}
		r.handles[modelID] = newConnectClientHandle(endpoint)
	}
	return r
}

// NewRegistryFromHandles constructs a Registry with caller-supplied
// ClientHandle implementations. Production wiring uses NewRegistry +
// env-var-sourced endpoint URLs; tests use NewRegistryFromHandles with
// fake handles to bypass the HTTP transport entirely.
func NewRegistryFromHandles(handles map[string]ClientHandle) *Registry {
	r := &Registry{handles: make(map[string]ClientHandle, len(handles))}
	for modelID, h := range handles {
		if h == nil {
			continue
		}
		r.handles[modelID] = h
	}
	return r
}

// LoadFromEnv is the startup-time entry point. Reads DEEPSEEK_ADAPTER_ENDPOINT
// (Story 4.1) and (in future Stories) QWEN/KIMI/GLM/DOUBAO/ERNIE siblings.
// Empty values omit the entry, which causes the gateway to fall through to
// the Story-3.3 mock path for that model id.
func LoadFromEnv() *Registry {
	return NewRegistry(map[string]string{
		DeepSeekModelID: os.Getenv(DeepSeekEndpointEnv),
	})
}

// Resolve looks up the ClientHandle for a model id. Returns (handle, true)
// on hit; (nil, false) on miss. The miss path is NOT an error — the
// gateway's handler falls back to the mock path on miss.
func (r *Registry) Resolve(modelID string) (ClientHandle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handles[modelID]
	return h, ok
}

// connectClientHandle is the production ClientHandle implementation backed
// by a Connect-RPC AdapterServiceClient.
type connectClientHandle struct {
	endpoint string
	httpc    *http.Client
	client   adapterv1connect.AdapterServiceClient
}

func newConnectClientHandle(endpoint string) *connectClientHandle {
	httpc := &http.Client{}
	return &connectClientHandle{
		endpoint: endpoint,
		httpc:    httpc,
		client:   adapterv1connect.NewAdapterServiceClient(httpc, endpoint),
	}
}

func (c *connectClientHandle) Chat(ctx contextLike, req *adapterv1.ChatRequest, headers http.Header) (Stream, error) {
	connectReq := connect.NewRequest(req)
	for k, vs := range headers {
		for _, v := range vs {
			connectReq.Header().Add(k, v)
		}
	}
	stream, err := c.client.Chat(ctx, connectReq)
	if err != nil {
		return nil, err
	}
	return &connectStreamAdapter{stream: stream}, nil
}

// connectStreamAdapter wraps connect.ServerStreamForClient[adapterv1.ChatChunk]
// in the local Stream surface (so callers don't need to import the connect
// package directly).
type connectStreamAdapter struct {
	stream *connect.ServerStreamForClient[adapterv1.ChatChunk]
}

func (s *connectStreamAdapter) Receive() bool        { return s.stream.Receive() }
func (s *connectStreamAdapter) Msg() *adapterv1.ChatChunk { return s.stream.Msg() }
func (s *connectStreamAdapter) Err() error           { return s.stream.Err() }
func (s *connectStreamAdapter) Close() error         { return s.stream.Close() }
