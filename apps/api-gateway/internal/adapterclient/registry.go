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
	"strings"
	"sync"

	"connectrpc.com/connect"
	obs "github.com/he-api/he-api/packages/go-observability"
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

// Story 9.5 — Vision model ids. BR-2.4 multi-model-id-per-service: the VL ids
// ride the EXISTING qwen / glm services (no new endpoints). qwen-vl-max →
// QWEN_ADAPTER_ENDPOINT (qwen now N=3: qwen-max/qwen-plus/qwen-vl-max share one
// ClientHandle via M2 endpoint-dedup); glm-4v → GLM_ADAPTER_ENDPOINT (glm now
// N=2: glm-4/glm-4v share one handle).
const (
	QwenVLMaxModelID = "qwen-vl-max"
	GLM4VModelID     = "glm-4v"
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

// Story 9.6 — Doubao ASR model id. The audio-transcription model rides the
// EXISTING Doubao service (Q-ASR-TOPOLOGY, single-service-per-vendor): it
// resolves to DOUBAO_ADAPTER_ENDPOINT and SHARES the Doubao ClientHandle via M2
// endpoint-dedup (the same handle that serves doubao-pro/doubao-lite Chat). The
// Doubao adapter serves the additive `Transcribe` RPC; the 5 non-Doubao
// adapters return CodeUnimplemented (never reached — only doubao-asr resolves
// here). The ASR upstream config (base URL + token + app-id + cluster) is
// adapter-side env, distinct from the Ark chat config.
const DoubaoASRModelID = "doubao-asr"

// Story 9.7 — Doubao TTS model id. The text-to-speech model rides the EXISTING
// Doubao service (Q-TTS-TOPOLOGY, single-service-per-vendor): it resolves to
// DOUBAO_ADAPTER_ENDPOINT and SHARES the Doubao ClientHandle via M2
// endpoint-dedup (the same handle that serves doubao-pro/doubao-lite Chat +
// doubao-asr Transcribe). The Doubao adapter serves the additive `Synthesize`
// RPC; the 5 non-Doubao adapters return CodeUnimplemented (never reached — only
// doubao-tts resolves here). The TTS upstream config (Volcano base URL + token +
// app-id + cluster + voice_type) is adapter-side env, distinct from the chat +
// ASR config.
const DoubaoTTSModelID = "doubao-tts"

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

// Transcriber is the Story-9.6 ASR seam alongside ClientHandle.Chat. It is a
// SEPARATE optional interface (not a method added to ClientHandle) so the
// existing chat-path ClientHandle fakes are unperturbed (additive blast
// radius): only the production connectClientHandle + the Doubao service satisfy
// it, and the ASR handler type-asserts the resolved handle to it. A handle that
// does not implement Transcriber (no adapter for an ASR-routed model) surfaces
// as a clean 502/unimplemented at the call site.
type Transcriber interface {
	Transcribe(ctx contextLike, req *adapterv1.TranscribeRequest, headers http.Header) (*adapterv1.TranscribeResponse, error)
}

// Synthesizer is the Story-9.7 TTS seam alongside ClientHandle.Chat +
// Transcriber. Like Transcriber it is a SEPARATE optional interface (not a
// method on ClientHandle) so existing chat-path fakes are unperturbed (additive
// blast radius): only the production connectClientHandle + the Doubao service
// satisfy it, and the speech handler type-asserts the resolved handle to it. A
// handle that does not implement Synthesizer (no adapter for a speech-routed
// model) surfaces as a clean 502/unimplemented at the call site.
type Synthesizer interface {
	Synthesize(ctx contextLike, req *adapterv1.SynthesizeRequest, headers http.Header) (*adapterv1.SynthesizeResponse, error)
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
// ExtraModelRoutesEnv appends model-id → vendor routes WITHOUT a code change.
//
// Why this exists: the built-in table below hardcodes model ids that vendors
// retire. In 2026-07 every id here (qwen-max, deepseek-v3, glm-4, …) had already
// been superseded upstream (qwen3.7-max, deepseek-v4-pro, glm-5.2, …), so every
// live call returned 403 Model.AccessDenied — the catalogue had silently rotted.
// A compiled-in list cannot track a vendor's release cadence; this env is the
// stop-gap until the DB-backed catalogue lands.
//
// Format: "modelID=vendor,modelID=vendor" where vendor ∈ deepseek|qwen|kimi|
// glm|doubao|ernie — the model routes to that vendor's adapter endpoint.
// Example: "qwen3.7-max=qwen,qwen3.7-plus=qwen,deepseek-v4-pro=deepseek"
const ExtraModelRoutesEnv = "HE_API_EXTRA_MODEL_ROUTES"

func LoadFromEnv() *Registry {
	qwenEndpoint := os.Getenv(QwenAdapterEndpointEnv)
	kimiEndpoint := os.Getenv(KimiAdapterEndpointEnv)
	doubaoEndpoint := os.Getenv(DoubaoAdapterEndpointEnv)
	glmEndpoint := os.Getenv(GLMAdapterEndpointEnv)
	routes := map[string]string{
		DeepSeekModelID:   os.Getenv(DeepSeekEndpointEnv),
		QwenMaxModelID:    qwenEndpoint,
		QwenPlusModelID:   qwenEndpoint,
		QwenVLMaxModelID:  qwenEndpoint, // Story 9.5 — VL rides the qwen service (N=3, shared handle)
		KimiV18kModelID:   kimiEndpoint,
		KimiV132kModelID:  kimiEndpoint,
		KimiV1128kModelID: kimiEndpoint,
		GLMModelID:        glmEndpoint,
		GLM4VModelID:      glmEndpoint, // Story 9.5 — VL rides the glm service (N=2, shared handle)
		DoubaoProModelID:  doubaoEndpoint,
		DoubaoLiteModelID: doubaoEndpoint,
		DoubaoASRModelID:  doubaoEndpoint, // Story 9.6 — ASR rides the doubao service (shared handle, M2 endpoint-dedup)
		DoubaoTTSModelID:  doubaoEndpoint, // Story 9.7 — TTS rides the doubao service (shared handle, M2 endpoint-dedup)
		ErnieModelID:      os.Getenv(ErnieAdapterEndpointEnv),
	}

	// Append operator-supplied routes for models the compiled table predates.
	// Unknown vendors and malformed pairs are skipped (a typo must not take the
	// gateway down); an empty endpoint means that vendor's adapter is unwired,
	// so the entry is dropped and the model falls through to the mock path.
	byVendor := map[string]string{
		"deepseek": os.Getenv(DeepSeekEndpointEnv),
		"qwen":     qwenEndpoint,
		"kimi":     kimiEndpoint,
		"glm":      glmEndpoint,
		"doubao":   doubaoEndpoint,
		"ernie":    os.Getenv(ErnieAdapterEndpointEnv),
	}
	for _, pair := range strings.Split(os.Getenv(ExtraModelRoutesEnv), ",") {
		modelID, vendor, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			continue
		}
		modelID = strings.TrimSpace(modelID)
		endpoint := byVendor[strings.ToLower(strings.TrimSpace(vendor))]
		if modelID == "" || endpoint == "" {
			continue
		}
		routes[modelID] = endpoint
	}

	return NewRegistry(routes)
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
	httpc := obs.NewHTTPClient() // Story 9.4 BR-TR-2/BR-TR-6 — instruments the upstream-model client span
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

// Transcribe implements the Story-9.6 Transcriber seam (unary ASR). Mirrors
// Chat: forwards the propagated headers (X-He-Request-Id etc.) and the
// TranscribeRequest over the additive Connect-RPC. Only the Doubao service
// serves this; the 5 non-Doubao adapters return CodeUnimplemented (never
// reached — only doubao-asr resolves to a handle here).
func (c *connectClientHandle) Transcribe(ctx contextLike, req *adapterv1.TranscribeRequest, headers http.Header) (*adapterv1.TranscribeResponse, error) {
	connectReq := connect.NewRequest(req)
	for k, vs := range headers {
		for _, v := range vs {
			connectReq.Header().Add(k, v)
		}
	}
	resp, err := c.client.Transcribe(ctx, connectReq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Synthesize implements the Story-9.7 Synthesizer seam (unary TTS). Mirrors
// Transcribe: forwards the propagated headers (X-He-Request-Id etc.) and the
// SynthesizeRequest over the additive Connect-RPC. Only the Doubao service
// serves this; the 5 non-Doubao adapters return CodeUnimplemented (never
// reached — only doubao-tts resolves to a handle here).
func (c *connectClientHandle) Synthesize(ctx contextLike, req *adapterv1.SynthesizeRequest, headers http.Header) (*adapterv1.SynthesizeResponse, error) {
	connectReq := connect.NewRequest(req)
	for k, vs := range headers {
		for _, v := range vs {
			connectReq.Header().Add(k, v)
		}
	}
	resp, err := c.client.Synthesize(ctx, connectReq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
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
