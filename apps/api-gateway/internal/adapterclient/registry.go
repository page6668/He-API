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
// Story 4.1 registers `deepseek-v3` → DEEPSEEK_ADAPTER_ENDPOINT.
// Story 4.2 registers `qwen-max` AND `qwen-plus` → QWEN_ADAPTER_ENDPOINT
// (BOTH model ids share ONE underlying ClientHandle per BR-1.10 +
// Architect Round 1 M2 endpoint-dedup ruling). The dedup refactor in
// NewRegistry preserves Story-4.1 single-endpoint behaviour bit-for-bit
// (one modelID → one handle).
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
// DeepSeek adapter's gRPC endpoint. Stories 4.2-4.6 add sibling env vars
// (QWEN_/KIMI_/GLM_/DOUBAO_/ERNIE_) following the same pattern; Epic-4
// brand-prefix cascade closes at Story 4.6.
const DeepSeekEndpointEnv = "DEEPSEEK_ADAPTER_ENDPOINT"

// DeepSeekModelID is the canonical model identifier the gateway accepts
// for the DeepSeek adapter.
const DeepSeekModelID = "deepseek-v3"

// Story 4.2 — Qwen (Alibaba DashScope) adapter constants.
//
// BR-1.10 multi-model-id-per-service dispatch: BOTH QwenMaxModelID and
// QwenPlusModelID map to the SAME QWEN_ADAPTER_ENDPOINT and therefore
// share ONE ClientHandle instance (M2 endpoint-dedup, see NewRegistry).
// The adapter receives `req.Model` verbatim and forwards it to
// upstream's `model` body field.
const (
	QwenMaxModelID         = "qwen-max"
	QwenPlusModelID        = "qwen-plus"
	QwenAdapterEndpointEnv = "QWEN_ADAPTER_ENDPOINT"
)

// Story 4.3 — Kimi (Moonshot AI) adapter constants.
//
// BR-1.10 multi-model-id-per-service dispatch extended to N=3: ALL THREE
// KimiV18kModelID + KimiV132kModelID + KimiV1128kModelID map to the SAME
// KIMI_ADAPTER_ENDPOINT and therefore share ONE ClientHandle instance
// (M2 endpoint-dedup, see NewRegistry — scales monomorphically from N=2
// Qwen to N=3 Kimi). The adapter receives `req.Model` verbatim and
// forwards it to upstream's `model` body field.
//
// Architect Round 1 OQ-4.3-2 ruling: env-var naming follows the adapter
// service brand (KIMI_*) NOT the model-id prefix (moonshot-v1-*). Kimi is
// the consumer-facing product brand; Moonshot is the company.
const (
	KimiV18kModelID        = "moonshot-v1-8k"
	KimiV132kModelID       = "moonshot-v1-32k"
	KimiV1128kModelID      = "moonshot-v1-128k"
	KimiAdapterEndpointEnv = "KIMI_ADAPTER_ENDPOINT"
)

// Story 4.4 — GLM (Zhipu AI) adapter constants.
//
// BR-1.10 multi-model-id-per-service dispatch DEGENERATE N=1 case:
// `glm-4` is the SOLE model id hosted by adapter-glm — collapses from
// Story-4.3's N=3 back to Story-4.1's N=1. The Story-4.2 M2 endpoint-
// dedup branch in NewRegistry handles N=1 cleanly (byEndpoint map has
// one entry, no dedup occurs).
//
// Architect Round 1 OQ-4.4-2 ruling: env-var naming follows the adapter
// service brand (GLM_*) per Story-4.2 OQ-4.2-6 cascade text (brand wins
// over company name `zhipu`; cascades to Stories 4.5-4.6 DOUBAO_* +
// ERNIE_*).
const (
	GLMModelID            = "glm-4"
	GLMAdapterEndpointEnv = "GLM_ADAPTER_ENDPOINT"
)

// Story 4.5 — Doubao (Volcengine Ark v3) adapter constants.
//
// BR-1.10 multi-model-id-per-service dispatch N=2 case: BOTH
// DoubaoProModelID and DoubaoLiteModelID map to the SAME
// DOUBAO_ADAPTER_ENDPOINT and therefore share ONE ClientHandle instance
// (M2 endpoint-dedup, see NewRegistry — RESTORES the Story-4.2 N=2
// pattern after Story-4.4's N=1 degenerate). The adapter receives
// `req.Model` (= friendly id `doubao-pro` / `doubao-lite`) and rewrites
// it adapter-side to the Volcengine endpoint id via
// `apps/adapters/doubao/internal/upstream.EndpointMap.Lookup` per
// OQ-4.5-3 — this is the FIRST Epic-4 non-identity translate.
//
// Architect Round 1 OQ-4.5-2 ruling: env-var naming follows the adapter
// service brand (DOUBAO_*) per Story-4.2 OQ-4.2-6 cascade text (brand
// wins over platform name `volcengine` and company name `bytedance`).
const (
	DoubaoProModelID         = "doubao-pro"
	DoubaoLiteModelID        = "doubao-lite"
	DoubaoAdapterEndpointEnv = "DOUBAO_ADAPTER_ENDPOINT"
)

// Story 4.6 — Ernie (Baidu Qianfan v2 OpenAI-compat) adapter constants.
//
// BR-1.10 multi-model-id-per-service dispatch DEGENERATE N=1 case:
// `ernie-4.0` is the SOLE model id hosted by adapter-ernie — restores
// the Story-4.4 N=1 degenerate after Story-4.5's N=2 RESTORATION. The
// Story-4.2 M2 endpoint-dedup branch in NewRegistry handles N=1 cleanly
// (byEndpoint map has one entry, no dedup occurs — 4.6-UNIT-011
// SKIPPED-branch documentation test per Story-4.4 R9 ratification
// cascade).
//
// Architect Round 1 OQ-4.6-1 ratification: option (a) Qianfan v2
// OpenAI-compat endpoint (`https://qianfan.baidubce.com/v2/chat/completions`)
// — collapses Story 4.6 to a pure REUSE of Story-4.4 glm N=1 template
// (`glm`→`ernie` substitution + Helm chart + ArgoCD app). Identity-
// mapping translate per OQ-4.6-3.
//
// Architect Round 1 OQ-4.6-2 ruling: env-var naming follows the adapter
// service brand (ERNIE_*) per Story-4.2 OQ-4.2-6 cascade text (brand
// wins over family marketing name `wenxin` / 文心 and company name
// `baidu`; final Epic-4 cascade closure).
const (
	ErnieModelID            = "ernie-4.0"
	ErnieAdapterEndpointEnv = "ERNIE_ADAPTER_ENDPOINT"
)

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

// Stream is the iterator surface a ClientHandle returns.
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
//
// Architect Round 1 M2 endpoint-dedup (Story 4.2): when MULTIPLE model ids
// point to the SAME endpoint URL (e.g., qwen-max + qwen-plus both → the
// QWEN_ADAPTER_ENDPOINT single-service-per-vendor topology ratified by
// OQ-4.2-2), share the underlying ClientHandle instance instead of
// constructing one per modelID. This is mandatory for BR-1.10's "the same
// `adapter-qwen` Connect-RPC client handle was used" assertion AND lets
// the underlying *http.Client + HTTP/2 connection pool be reused across
// model-id dispatches to the same vendor.
//
// Story-4.1 single-endpoint behaviour (one modelID per endpoint) is
// preserved bit-for-bit — the dedup branch is a no-op for one-modelID-
// per-endpoint cases.
func NewRegistry(endpoints map[string]string) *Registry {
	r := &Registry{handles: make(map[string]ClientHandle, len(endpoints))}
	byEndpoint := make(map[string]ClientHandle, len(endpoints))
	for modelID, endpoint := range endpoints {
		if endpoint == "" {
			continue
		}
		h, ok := byEndpoint[endpoint]
		if !ok {
			h = newConnectClientHandle(endpoint)
			byEndpoint[endpoint] = h
		}
		r.handles[modelID] = h
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

// LoadFromEnv is the startup-time entry point. Reads:
//
//	DEEPSEEK_ADAPTER_ENDPOINT  (Story 4.1) → deepseek-v3
//	QWEN_ADAPTER_ENDPOINT      (Story 4.2) → qwen-max AND qwen-plus
//	KIMI_ADAPTER_ENDPOINT      (Story 4.3) → moonshot-v1-8k + 32k + 128k
//	GLM_ADAPTER_ENDPOINT       (Story 4.4) → glm-4 (N=1 degenerate)
//	DOUBAO_ADAPTER_ENDPOINT    (Story 4.5) → doubao-pro AND doubao-lite (N=2)
//	ERNIE_ADAPTER_ENDPOINT     (Story 4.6) → ernie-4.0 (N=1 degenerate;
//	                                          closes the six-vendor matrix)
//
// Empty values omit the entry, which causes the gateway to fall through
// to the Story-3.3 mock path for that model id.
func LoadFromEnv() *Registry {
	qwenEndpoint := os.Getenv(QwenAdapterEndpointEnv)
	kimiEndpoint := os.Getenv(KimiAdapterEndpointEnv)
	doubaoEndpoint := os.Getenv(DoubaoAdapterEndpointEnv)
	return NewRegistry(map[string]string{
		DeepSeekModelID:   os.Getenv(DeepSeekEndpointEnv),
		QwenMaxModelID:    qwenEndpoint,
		QwenPlusModelID:   qwenEndpoint,
		KimiV18kModelID:   kimiEndpoint,
		KimiV132kModelID:  kimiEndpoint,
		KimiV1128kModelID: kimiEndpoint,
		GLMModelID:        os.Getenv(GLMAdapterEndpointEnv),
		DoubaoProModelID:  doubaoEndpoint,
		DoubaoLiteModelID: doubaoEndpoint,
		ErnieModelID:      os.Getenv(ErnieAdapterEndpointEnv),
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

func (s *connectStreamAdapter) Receive() bool             { return s.stream.Receive() }
func (s *connectStreamAdapter) Msg() *adapterv1.ChatChunk { return s.stream.Msg() }
func (s *connectStreamAdapter) Err() error                { return s.stream.Err() }
func (s *connectStreamAdapter) Close() error              { return s.stream.Close() }
