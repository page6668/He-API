package modelscatalogue

// ModelSeed is one declared model in a Registry: its id + owning vendor, in
// the canonical advertised order. Capabilities are held separately (in the
// Registry.Capabilities map) so the BR-1.3 1:1 invariant between the two is a
// real, testable surface rather than a merged struct.
type ModelSeed struct {
	ID          string
	DisplayName string // reserved-for-future; empty in DefaultRegistry
	Vendor      string
}

// Registry is the raw catalogue seed: an ordered model list + a capability
// map keyed by model id. NewFromRegistry resolves the two into a Catalogue,
// enforcing the 1:1 invariant.
type Registry struct {
	Models       []ModelSeed
	Capabilities map[string]Capabilities
}

// DefaultRegistry is the BR-1.4 authoritative 11-entry catalogue, lifted
// verbatim from the Story-4.7 api-gateway `modelsCatalogue` +
// `capabilitiesByModelID` (Architect Round-1 OQ1 ratified 11 rows — NOT 9 —
// and OQ-4.7-3 ratified the capability values). Declaration order is
// load-bearing (Story-3.5 BR-1.10): the gateway emits `/v1/models` in this
// order and SDK consumers may use index-based assertions.
//
// The three he-router-* virtual entries report the UNION of their candidate
// pool (marketing-correct); they are routing meta-models, not upstream
// targets — Epic-6 routing-svc never routes TO them (that support is Story
// 6.2 scope).
var DefaultRegistry = Registry{
	Models: []ModelSeed{
		{ID: "qwen-max", Vendor: "alibaba"},
		{ID: "qwen-plus", Vendor: "alibaba"},
		{ID: "deepseek-v3", Vendor: "deepseek"},
		{ID: "moonshot-v1-128k", Vendor: "moonshot"},
		{ID: "glm-4", Vendor: "zhipu"},
		{ID: "doubao-pro", Vendor: "bytedance"},
		{ID: "doubao-lite", Vendor: "bytedance"},
		{ID: "ernie-4.0", Vendor: "baidu"},
		{ID: "he-router-cost", Vendor: "he-api"},
		{ID: "he-router-quality", Vendor: "he-api"},
		{ID: "he-router-latency", Vendor: "he-api"},
		// Story 9.5 — the first Vision models. Appended at the END so the
		// existing index-based ordering (positions 0-10) is preserved (BR-1.10
		// declaration-order is load-bearing). They ride the existing qwen / glm
		// adapter services (BR-2.4 multi-model-id) and advertise vision:true.
		{ID: "qwen-vl-max", Vendor: "alibaba"},
		{ID: "glm-4v", Vendor: "zhipu"},
		// Story 9.6 — the first audio model (ASR). Appended at the END so the
		// existing index ordering (0-12) is preserved (BR-1.10). It is
		// Chat:false / Transcription:true — the ONLY non-chat model in the
		// catalogue, which is exactly why the BR-1.6 Chat:true fence is needed
		// on /v1/chat/completions. Rides the existing doubao adapter service.
		{ID: "doubao-asr", Vendor: "bytedance"},
		// Story 9.7 — the first TTS model. Appended at the END so the existing
		// index ordering (0-13) is preserved (BR-1.10). It is Chat:false /
		// Transcription:false / Speech:true — dispatchable ONLY on
		// /v1/audio/speech (the BR-1.6 fences exclude it from chat + ASR). Rides
		// the existing doubao adapter service.
		{ID: "doubao-tts", Vendor: "bytedance"},
	},
	Capabilities: map[string]Capabilities{
		"qwen-max":          {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"qwen-plus":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"deepseek-v3":       {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 65536, MaxOutputTokens: 8192},
		"moonshot-v1-128k":  {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"glm-4":             {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"doubao-pro":        {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"doubao-lite":       {Chat: true, Streaming: true, FunctionCalling: false, Vision: false, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 4096},
		"ernie-4.0":         {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 8192, MaxOutputTokens: 2048},
		"he-router-cost":    {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"he-router-quality": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		"he-router-latency": {Chat: true, Streaming: true, FunctionCalling: true, Vision: false, JSONMode: true, ContextWindowTokens: 131072, MaxOutputTokens: 8192},
		// Story 9.5 — Vision:true is the new capability. FunctionCalling/JSONMode
		// left false (this story adds image INPUT only; it does not claim tool/JSON
		// support for the VL ids). Streaming:true (BR-2.7 — output is text deltas).
		"qwen-vl-max": {Chat: true, Streaming: true, FunctionCalling: false, Vision: true, JSONMode: false, ContextWindowTokens: 32768, MaxOutputTokens: 8192},
		"glm-4v":      {Chat: true, Streaming: true, FunctionCalling: false, Vision: true, JSONMode: false, ContextWindowTokens: 8192, MaxOutputTokens: 4096},
		// Story 9.6 — ASR id. Chat:false (NOT a chat model — gated off
		// /v1/chat/completions by the BR-1.6 fence), Transcription:true (gates
		// ON /v1/audio/transcriptions). No token window (audio has no token
		// dimension); not streaming (Whisper transcription is single-shot).
		"doubao-asr": {Chat: false, Streaming: false, FunctionCalling: false, Vision: false, JSONMode: false, Transcription: true, ContextWindowTokens: 0, MaxOutputTokens: 0},
		// Story 9.7 — TTS id. Chat:false / Transcription:false (gated off
		// /v1/chat/completions + /v1/audio/transcriptions by the 9.6 fences),
		// Speech:true (gates ON /v1/audio/speech). No token window (audio has no
		// token dimension); not streaming (9.7 returns a single audio body).
		"doubao-tts": {Chat: false, Streaming: false, FunctionCalling: false, Vision: false, JSONMode: false, Transcription: false, Speech: true, ContextWindowTokens: 0, MaxOutputTokens: 0},
	},
}

// DefaultCatalogue is the resolved DefaultRegistry. Built at package init, so
// any future drift in DefaultRegistry (a model without a capability row, or a
// capability orphan) panics at process start — preserving the Story-4.7
// boot-fail posture for both the api-gateway and routing-svc.
var DefaultCatalogue = NewFromRegistry(DefaultRegistry)
