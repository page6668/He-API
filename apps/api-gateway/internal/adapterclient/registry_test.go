package adapterclient

import (
	"sync"
	"testing"
)

// 4.1-UNIT-011 (P0) — BR-1.1 resolution semantics. "deepseek-v3" resolves
// to the configured endpoint; concurrent reads are race-clean (100 readers
// covering R7 concurrency mitigation).
func TestRegistry_Resolve_DeepSeekV3(t *testing.T) {
	reg := NewRegistry(map[string]string{
		"deepseek-v3": "https://adapter-deepseek.he-api-adapters.svc.cluster.local:8080",
	})
	h, ok := reg.Resolve("deepseek-v3")
	if !ok {
		t.Fatalf("Resolve(deepseek-v3) ok=false, want true")
	}
	if h == nil {
		t.Fatalf("Resolve returned nil handle")
	}
}

func TestRegistry_Resolve_100ConcurrentReaders_NoRace(t *testing.T) {
	reg := NewRegistry(map[string]string{"deepseek-v3": "https://x"})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = reg.Resolve("deepseek-v3")
			}
		}()
	}
	wg.Wait()
}

// 4.1-UNIT-012 (P0) — BR-1.1 negative path. Other models return ok=false;
// empty env var → entry omitted from the registry → ok=false.
func TestRegistry_Resolve_UnknownModel_ReturnsOkFalse(t *testing.T) {
	reg := NewRegistry(map[string]string{"deepseek-v3": "https://x"})
	if h, ok := reg.Resolve("glm-4"); ok {
		t.Fatalf("Resolve(glm-4) ok=true (h=%v), want false (fall through to mock)", h)
	}
	if _, ok := reg.Resolve(""); ok {
		t.Fatalf("Resolve(\"\") ok=true, want false")
	}
}

func TestRegistry_LoadFromEnv_PicksUpDeepseekEndpointWhenSet(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "https://adapter-deepseek.test:8080")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	if h, ok := reg.Resolve("deepseek-v3"); !ok || h == nil {
		t.Fatalf("env-loaded registry missing deepseek-v3 entry: ok=%v h=%v", ok, h)
	}
}

func TestRegistry_LoadFromEnv_EmptyEnv_OmitsEntry(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	t.Setenv("ERNIE_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	if _, ok := reg.Resolve("deepseek-v3"); ok {
		t.Fatalf("empty DEEPSEEK_ADAPTER_ENDPOINT should result in no deepseek-v3 registration")
	}
	if _, ok := reg.Resolve("qwen-max"); ok {
		t.Fatalf("empty QWEN_ADAPTER_ENDPOINT should result in no qwen-max registration")
	}
	if _, ok := reg.Resolve("qwen-plus"); ok {
		t.Fatalf("empty QWEN_ADAPTER_ENDPOINT should result in no qwen-plus registration")
	}
	for _, kimi := range []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"} {
		if _, ok := reg.Resolve(kimi); ok {
			t.Fatalf("empty KIMI_ADAPTER_ENDPOINT should result in no %s registration", kimi)
		}
	}
	if _, ok := reg.Resolve("glm-4"); ok {
		t.Fatalf("empty GLM_ADAPTER_ENDPOINT should result in no glm-4 registration")
	}
	if _, ok := reg.Resolve("doubao-pro"); ok {
		t.Fatalf("empty DOUBAO_ADAPTER_ENDPOINT should result in no doubao-pro registration")
	}
	if _, ok := reg.Resolve("doubao-lite"); ok {
		t.Fatalf("empty DOUBAO_ADAPTER_ENDPOINT should result in no doubao-lite registration")
	}
	if _, ok := reg.Resolve("ernie-4.0"); ok {
		t.Fatalf("empty ERNIE_ADAPTER_ENDPOINT should result in no ernie-4.0 registration")
	}
}

// 4.1-BLIND-BOUNDARY-003 (P1) — case-sensitive model-id resolution.
func TestRegistry_Resolve_UppercaseModelId_MissesRegistry(t *testing.T) {
	reg := NewRegistry(map[string]string{"deepseek-v3": "https://x"})
	for _, id := range []string{"DeepSeek-V3", "DEEPSEEK-V3", "Deepseek-V3"} {
		if _, ok := reg.Resolve(id); ok {
			t.Fatalf("Resolve(%q) ok=true; expected case-sensitive miss", id)
		}
	}
}

// 4.2-UNIT-011 (P0) — BR-1.10 multi-model-id-per-service dispatch +
// Architect Round 1 M2 endpoint-dedup. qwen-max AND qwen-plus pointing to
// the SAME QWEN_ADAPTER_ENDPOINT must resolve to the IDENTICAL ClientHandle
// pointer (so the underlying *http.Client + connection pool is shared).
func TestRegistry_QwenMaxAndQwenPlus_ShareSameHandle_M2_EndpointDedup(t *testing.T) {
	const endpoint = "https://adapter-qwen.he-api-adapters.svc.cluster.local:8080"
	reg := NewRegistry(map[string]string{
		QwenMaxModelID:  endpoint,
		QwenPlusModelID: endpoint,
	})
	hMax, okMax := reg.Resolve(QwenMaxModelID)
	hPlus, okPlus := reg.Resolve(QwenPlusModelID)
	if !okMax || !okPlus {
		t.Fatalf("both qwen entries must resolve: okMax=%v okPlus=%v", okMax, okPlus)
	}
	if hMax != hPlus {
		t.Fatalf("qwen-max and qwen-plus must return the IDENTICAL ClientHandle (M2 endpoint-dedup); got hMax=%p hPlus=%p", hMax, hPlus)
	}
}

// 4.2-UNIT-012 (P0) — Different endpoints MUST NOT be deduplicated. Story
// 4.1 regression guard — deepseek-v3 and qwen-max must continue to live
// behind separate ClientHandle instances (different *http.Client +
// connection pools per vendor).
func TestRegistry_DeepSeekAndQwen_DistinctHandles(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID: "https://adapter-deepseek.test:8080",
		QwenMaxModelID:  "https://adapter-qwen.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hQwen, okQwen := reg.Resolve(QwenMaxModelID)
	if !okDS || !okQwen {
		t.Fatalf("both entries must resolve: okDS=%v okQwen=%v", okDS, okQwen)
	}
	if hDS == hQwen {
		t.Fatalf("deepseek-v3 and qwen-max must use DISTINCT ClientHandles (different endpoints)")
	}
}

// 4.2-UNIT-013 (P0) — Unknown model id (e.g., glm-4 not yet wired by
// Story 4.4) returns ok=false → gateway falls through to the mock path.
// Regression guard alongside Qwen registration.
func TestRegistry_UnknownModel_WithQwenRegistered_ReturnsOkFalse(t *testing.T) {
	const endpoint = "https://adapter-qwen.test:8080"
	reg := NewRegistry(map[string]string{
		QwenMaxModelID:  endpoint,
		QwenPlusModelID: endpoint,
	})
	if _, ok := reg.Resolve("glm-4"); ok {
		t.Fatalf("Resolve(glm-4) ok=true with qwen registered; want false")
	}
	if _, ok := reg.Resolve("kimi-8k"); ok {
		t.Fatalf("Resolve(kimi-8k) ok=true; want false")
	}
}

// 4.2-UNIT-013-deepseek-regression — Story 4.1 deepseek-v3 entry MUST
// continue to resolve to its own handle even when Story 4.2's Qwen
// entries are co-registered.
func TestRegistry_DeepSeekStillResolves_WithQwenCoRegistered(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID: "https://adapter-deepseek.test:8080",
		QwenMaxModelID:  "https://adapter-qwen.test:8080",
		QwenPlusModelID: "https://adapter-qwen.test:8080",
	})
	h, ok := reg.Resolve(DeepSeekModelID)
	if !ok || h == nil {
		t.Fatalf("Story 4.1 regression: deepseek-v3 missing from co-registered registry")
	}
}

// 4.2-UNIT-013-oq-4.2-6 — env-var naming verification (Architect Round 1
// OQ-4.2-6 ratification: QWEN_UPSTREAM_API_KEY is the per-vendor secret
// env var on the adapter side; QWEN_ADAPTER_ENDPOINT is the gateway-side
// env var. THIS test only covers the gateway-side QWEN_ADAPTER_ENDPOINT.
func TestRegistry_LoadFromEnv_QwenEndpointEnvName_IsLiteralString(t *testing.T) {
	if QwenAdapterEndpointEnv != "QWEN_ADAPTER_ENDPOINT" {
		t.Fatalf("QwenAdapterEndpointEnv = %q, want literal \"QWEN_ADAPTER_ENDPOINT\" (model-family naming per OQ-4.2-6)", QwenAdapterEndpointEnv)
	}
	if QwenMaxModelID != "qwen-max" {
		t.Fatalf("QwenMaxModelID = %q, want \"qwen-max\"", QwenMaxModelID)
	}
	if QwenPlusModelID != "qwen-plus" {
		t.Fatalf("QwenPlusModelID = %q, want \"qwen-plus\"", QwenPlusModelID)
	}
}

// 4.2-UNIT-011-loadfromenv — LoadFromEnv with QWEN_ADAPTER_ENDPOINT set
// populates BOTH qwen-max and qwen-plus entries with the same handle.
func TestRegistry_LoadFromEnv_QwenEndpointSet_PopulatesBothModelIds(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "https://adapter-qwen.test:8080")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	hMax, okMax := reg.Resolve("qwen-max")
	hPlus, okPlus := reg.Resolve("qwen-plus")
	if !okMax || !okPlus {
		t.Fatalf("LoadFromEnv with QWEN_ADAPTER_ENDPOINT must populate both qwen entries: okMax=%v okPlus=%v", okMax, okPlus)
	}
	if hMax != hPlus {
		t.Fatalf("LoadFromEnv must produce identical handles for qwen-max + qwen-plus (M2)")
	}
}

// 4.3-UNIT-011 (P0) — BR-1.10 N=3 multi-model-id-per-service dispatch +
// Architect Round 1 M2 endpoint-dedup scaled to N=3 (Story-4.2 N=2 →
// Story-4.3 N=3 per OQ-4.3-5). ALL THREE moonshot-v1-{8k,32k,128k}
// pointing to the SAME KIMI_ADAPTER_ENDPOINT MUST resolve to the
// IDENTICAL ClientHandle (assert.Same chain across all 3 handles).
func TestRegistry_AllThreeKimi_ShareSameHandle_M2_N3_EndpointDedup(t *testing.T) {
	const endpoint = "https://adapter-kimi.he-api-adapters.svc.cluster.local:8080"
	reg := NewRegistry(map[string]string{
		KimiV18kModelID:   endpoint,
		KimiV132kModelID:  endpoint,
		KimiV1128kModelID: endpoint,
	})
	h8k, ok8k := reg.Resolve(KimiV18kModelID)
	h32k, ok32k := reg.Resolve(KimiV132kModelID)
	h128k, ok128k := reg.Resolve(KimiV1128kModelID)
	if !ok8k || !ok32k || !ok128k {
		t.Fatalf("all three Kimi entries must resolve: ok8k=%v ok32k=%v ok128k=%v", ok8k, ok32k, ok128k)
	}
	// assert.Same chain per OQ-4.3-5: every pair of handles must be
	// identical (M2 byEndpoint map dedups all three to one handle).
	if h8k != h32k {
		t.Fatalf("moonshot-v1-8k and moonshot-v1-32k must share handle (M2 N=3); got h8k=%p h32k=%p", h8k, h32k)
	}
	if h32k != h128k {
		t.Fatalf("moonshot-v1-32k and moonshot-v1-128k must share handle (M2 N=3); got h32k=%p h128k=%p", h32k, h128k)
	}
}

// 4.3-UNIT-012 (P0) — Story 4.1 regression guard: deepseek-v3 entry MUST
// continue to resolve to a DISTINCT ClientHandle from Kimi.
func TestRegistry_DeepSeekAndKimi_DistinctHandles(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID:   "https://adapter-deepseek.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hKimi, okKimi := reg.Resolve(KimiV18kModelID)
	if !okDS || !okKimi {
		t.Fatalf("both entries must resolve: okDS=%v okKimi=%v", okDS, okKimi)
	}
	if hDS == hKimi {
		t.Fatalf("deepseek-v3 and moonshot-v1-8k must use DISTINCT ClientHandles (different endpoints)")
	}
}

// 4.3-UNIT-013 (P0) — Story 4.2 regression guard: qwen-* entries MUST
// continue to resolve to handles DISTINCT from Kimi handles.
func TestRegistry_QwenAndKimi_DistinctHandles(t *testing.T) {
	reg := NewRegistry(map[string]string{
		QwenMaxModelID:    "https://adapter-qwen.test:8080",
		QwenPlusModelID:   "https://adapter-qwen.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
	})
	hQwen, _ := reg.Resolve(QwenMaxModelID)
	hKimi8k, _ := reg.Resolve(KimiV18kModelID)
	hKimi32k, _ := reg.Resolve(KimiV132kModelID)
	if hQwen == hKimi8k || hQwen == hKimi32k {
		t.Fatalf("qwen and kimi handles must be DISTINCT; got hQwen=%p hKimi8k=%p hKimi32k=%p", hQwen, hKimi8k, hKimi32k)
	}
}

// 4.3-UNIT-013-unknown — Unknown model id (e.g., glm-4 not yet wired by
// Story 4.4) returns ok=false → gateway falls through to the mock path.
// Regression guard alongside Kimi registration.
func TestRegistry_UnknownModel_WithKimiRegistered_ReturnsOkFalse(t *testing.T) {
	const endpoint = "https://adapter-kimi.test:8080"
	reg := NewRegistry(map[string]string{
		KimiV18kModelID:   endpoint,
		KimiV132kModelID:  endpoint,
		KimiV1128kModelID: endpoint,
	})
	if _, ok := reg.Resolve("glm-4"); ok {
		t.Fatalf("Resolve(glm-4) ok=true with kimi registered; want false")
	}
	if _, ok := reg.Resolve("moonshot-v1-256k"); ok {
		t.Fatalf("Resolve(moonshot-v1-256k) ok=true (typo); want false")
	}
}

// 4.3-UNIT-oq-4.3-2 — Architect Round 1 OQ-4.3-2 ratification lock-in:
// env-var naming follows the adapter service BRAND (KIMI_*), NOT the
// model-id prefix (moonshot-v1-*). Constants must be literal strings.
func TestRegistry_KimiEndpointEnvName_IsLiteralBrandString(t *testing.T) {
	if KimiAdapterEndpointEnv != "KIMI_ADAPTER_ENDPOINT" {
		t.Fatalf("KimiAdapterEndpointEnv = %q, want literal \"KIMI_ADAPTER_ENDPOINT\" (OQ-4.3-2 brand-wins)", KimiAdapterEndpointEnv)
	}
	if KimiV18kModelID != "moonshot-v1-8k" {
		t.Fatalf("KimiV18kModelID = %q, want \"moonshot-v1-8k\"", KimiV18kModelID)
	}
	if KimiV132kModelID != "moonshot-v1-32k" {
		t.Fatalf("KimiV132kModelID = %q, want \"moonshot-v1-32k\"", KimiV132kModelID)
	}
	if KimiV1128kModelID != "moonshot-v1-128k" {
		t.Fatalf("KimiV1128kModelID = %q, want \"moonshot-v1-128k\"", KimiV1128kModelID)
	}
}

// 4.3-UNIT-011-loadfromenv — LoadFromEnv with KIMI_ADAPTER_ENDPOINT set
// populates ALL THREE Kimi entries with the same handle (N=3 dedup).
func TestRegistry_LoadFromEnv_KimiEndpointSet_PopulatesAllThreeModelIds(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "https://adapter-kimi.test:8080")
	reg := LoadFromEnv()
	h8k, ok8k := reg.Resolve("moonshot-v1-8k")
	h32k, ok32k := reg.Resolve("moonshot-v1-32k")
	h128k, ok128k := reg.Resolve("moonshot-v1-128k")
	if !ok8k || !ok32k || !ok128k {
		t.Fatalf("LoadFromEnv with KIMI_ADAPTER_ENDPOINT must populate ALL three Kimi entries: ok8k=%v ok32k=%v ok128k=%v", ok8k, ok32k, ok128k)
	}
	if h8k != h32k || h32k != h128k {
		t.Fatalf("LoadFromEnv must produce identical handles for all three Kimi sizes (M2 N=3)")
	}
}

// 4.3-INT-016 (P0) — cross-vendor regression: registering Kimi entries
// MUST NOT shadow Story-4.1 deepseek-v3 OR Story-4.2 qwen-* entries.
// Each vendor's model ids continue to resolve to their own handle.
func TestRegistry_AllThreeVendorsCoRegistered_NoCrossVendorShadowing(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID:   "https://adapter-deepseek.test:8080",
		QwenMaxModelID:    "https://adapter-qwen.test:8080",
		QwenPlusModelID:   "https://adapter-qwen.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hQwen, okQwen := reg.Resolve(QwenMaxModelID)
	hQwenPlus, okQwenPlus := reg.Resolve(QwenPlusModelID)
	hKimi8k, okKimi8k := reg.Resolve(KimiV18kModelID)
	hKimi128k, okKimi128k := reg.Resolve(KimiV1128kModelID)
	if !okDS || !okQwen || !okQwenPlus || !okKimi8k || !okKimi128k {
		t.Fatalf("co-registered registry missing one or more entries: okDS=%v okQwen=%v okQwenPlus=%v okKimi8k=%v okKimi128k=%v", okDS, okQwen, okQwenPlus, okKimi8k, okKimi128k)
	}
	if hQwen != hQwenPlus {
		t.Fatalf("qwen-max and qwen-plus must share handle (Story-4.2 invariant preserved)")
	}
	if hKimi8k != hKimi128k {
		t.Fatalf("kimi 8k and 128k must share handle (Story-4.3 N=3 invariant)")
	}
	if hDS == hQwen || hDS == hKimi8k || hQwen == hKimi8k {
		t.Fatalf("cross-vendor handles must be DISTINCT")
	}
}

// 4.4-UNIT-011 (P0) — Architect Round 1 R9 anchor: the Story-4.2 M2
// `NewRegistry` byEndpoint endpoint-dedup branch handles the N=1
// degenerate case CLEANLY. When only `glm-4` maps to
// `GLM_ADAPTER_ENDPOINT`, the byEndpoint map has exactly one entry, the
// `if !ok { newConnectClientHandle }` branch executes once, and no
// `assert.Same` chain is triggered. This is the documented SKIPPED-
// branch test (the assert.Same chain from Stories 4.2/4.3 is N/A at
// N=1 — degenerate but tractable).
func TestRegistry_GLM4_M2_EndpointDedup_N1_NoOp_R9(t *testing.T) {
	const endpoint = "https://adapter-glm.he-api-adapters.svc.cluster.local:8080"
	reg := NewRegistry(map[string]string{
		GLMModelID: endpoint,
	})
	h, ok := reg.Resolve(GLMModelID)
	if !ok {
		t.Fatalf("Resolve(glm-4) ok=false, want true")
	}
	if h == nil {
		t.Fatalf("Resolve returned nil handle")
	}
	// SKIPPED-branch documentation: at N=1 the byEndpoint map has size 1,
	// the dedup branch fires once, and there is no second handle to
	// compare against. The Story-4.2 M2 mechanism is therefore a
	// well-behaved no-op for N=1 single-model-id vendors.
	t.Logf("N=1 endpoint-dedup no-op verified — Resolve(glm-4) returned a single handle without dedup work")
}

// 4.4-UNIT-012 (P0) — basic resolution path: Resolve(glm-4) returns a
// non-nil handle; Resolve of a non-registered id returns (nil, false).
func TestRegistry_GLM4_Resolve(t *testing.T) {
	reg := NewRegistry(map[string]string{
		GLMModelID: "https://adapter-glm.he-api-adapters.svc.cluster.local:8080",
	})
	if h, ok := reg.Resolve(GLMModelID); !ok || h == nil {
		t.Fatalf("Resolve(glm-4) returned (%v,%v); want (non-nil,true)", h, ok)
	}
	if h, ok := reg.Resolve("not-in-registry"); ok {
		t.Fatalf("Resolve(\"not-in-registry\") ok=true (h=%v), want false", h)
	}
}

// 4.4-UNIT-013 (P0) — OQ-4.4-2 ratification lock-in: env-var naming
// follows the adapter service BRAND (`GLM_*`), NOT the company name
// (`ZHIPU_*`). Constants must be literal strings.
func TestRegistry_GLMEndpointEnvName_IsLiteralBrandString(t *testing.T) {
	if GLMAdapterEndpointEnv != "GLM_ADAPTER_ENDPOINT" {
		t.Fatalf("GLMAdapterEndpointEnv = %q, want literal \"GLM_ADAPTER_ENDPOINT\" (OQ-4.4-2 brand-wins)", GLMAdapterEndpointEnv)
	}
	if GLMModelID != "glm-4" {
		t.Fatalf("GLMModelID = %q, want \"glm-4\"", GLMModelID)
	}
}

// 4.4-UNIT-014 — `LoadFromEnv` reads `GLM_ADAPTER_ENDPOINT` correctly +
// falls back gracefully on unset.
func TestRegistry_LoadFromEnv_GLMEndpointSet_PopulatesGLM4(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "https://adapter-glm.test:8080")
	reg := LoadFromEnv()
	if h, ok := reg.Resolve("glm-4"); !ok || h == nil {
		t.Fatalf("LoadFromEnv with GLM_ADAPTER_ENDPOINT must populate glm-4: ok=%v h=%v", ok, h)
	}
	// Other vendors with empty endpoints stay omitted.
	if _, ok := reg.Resolve("deepseek-v3"); ok {
		t.Fatalf("empty DEEPSEEK_ADAPTER_ENDPOINT must not register deepseek-v3")
	}
}

// 4.5-UNIT-013 (P0) — R9 N=2 RESTORATION: Story-4.2 M2 `NewRegistry`
// byEndpoint endpoint-dedup branch returns to actually-executing form
// after Story-4.4's N=1 degenerate. BOTH doubao-pro AND doubao-lite
// pointing to the SAME DOUBAO_ADAPTER_ENDPOINT MUST resolve to the
// IDENTICAL ClientHandle (assert.Same chain — REUSE Story-4.2
// 4.2-UNIT-013 pattern verbatim per OQ-4.3-5 cascade-locked policy).
func TestRegistry_DoubaoProAndLite_ShareSameHandle_M2_N2_RESTORATION(t *testing.T) {
	const endpoint = "https://adapter-doubao.he-api-adapters.svc.cluster.local:8080"
	reg := NewRegistry(map[string]string{
		DoubaoProModelID:  endpoint,
		DoubaoLiteModelID: endpoint,
	})
	hPro, okPro := reg.Resolve(DoubaoProModelID)
	hLite, okLite := reg.Resolve(DoubaoLiteModelID)
	if !okPro || !okLite {
		t.Fatalf("both doubao entries must resolve: okPro=%v okLite=%v", okPro, okLite)
	}
	if hPro != hLite {
		t.Fatalf("doubao-pro and doubao-lite must return the IDENTICAL ClientHandle (M2 endpoint-dedup N=2 RESTORATION); got hPro=%p hLite=%p", hPro, hLite)
	}
}

// 4.5-UNIT-012 (P0) — OQ-4.5-2 ratification lock-in: env-var naming
// follows the adapter service BRAND (`DOUBAO_*`), NOT the platform name
// (`VOLCENGINE_*`) or company name (`BYTEDANCE_*`). Constants must be
// literal strings.
func TestRegistry_DoubaoConstants_BrandPrefix(t *testing.T) {
	if DoubaoProModelID != "doubao-pro" {
		t.Fatalf("DoubaoProModelID = %q, want \"doubao-pro\"", DoubaoProModelID)
	}
	if DoubaoLiteModelID != "doubao-lite" {
		t.Fatalf("DoubaoLiteModelID = %q, want \"doubao-lite\"", DoubaoLiteModelID)
	}
	if DoubaoAdapterEndpointEnv != "DOUBAO_ADAPTER_ENDPOINT" {
		t.Fatalf("DoubaoAdapterEndpointEnv = %q, want literal \"DOUBAO_ADAPTER_ENDPOINT\" (OQ-4.5-2 brand-wins)", DoubaoAdapterEndpointEnv)
	}
}

// 4.5-UNIT-014 — LoadFromEnv reads DOUBAO_ADAPTER_ENDPOINT correctly:
// (a) when set → both pro+lite entries populated; (b) when unset → both
// entries absent (registry gateway-startup proceeds; all doubao-* traffic
// falls back to mock per Story-3.3/3.4/3.5).
func TestRegistry_LoadFromEnv_DoubaoEndpointSet_PopulatesBothEntries(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "https://adapter-doubao.test:8080")
	reg := LoadFromEnv()
	hPro, okPro := reg.Resolve(DoubaoProModelID)
	hLite, okLite := reg.Resolve(DoubaoLiteModelID)
	if !okPro || !okLite {
		t.Fatalf("LoadFromEnv with DOUBAO_ADAPTER_ENDPOINT must populate both doubao entries: okPro=%v okLite=%v", okPro, okLite)
	}
	if hPro != hLite {
		t.Fatalf("LoadFromEnv must produce identical handles for doubao-pro + doubao-lite (M2 N=2)")
	}
	// Empty siblings stay omitted.
	if _, ok := reg.Resolve(DeepSeekModelID); ok {
		t.Fatalf("empty DEEPSEEK_ADAPTER_ENDPOINT must not register deepseek-v3")
	}
}

func TestRegistry_LoadFromEnv_DoubaoEndpointUnset_OmitsEntries(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	if _, ok := reg.Resolve(DoubaoProModelID); ok {
		t.Fatalf("empty DOUBAO_ADAPTER_ENDPOINT must not register doubao-pro")
	}
	if _, ok := reg.Resolve(DoubaoLiteModelID); ok {
		t.Fatalf("empty DOUBAO_ADAPTER_ENDPOINT must not register doubao-lite")
	}
}

// 4.5-INT-009 (P0) — FIVE-vendor cross-vendor regression (R8 + R9):
// registering the doubao entries MUST NOT shadow Stories-4.1/4.2/4.3/4.4
// entries. Each vendor's model ids continue to resolve to their own
// DISTINCT handle. M2 dedup invariant verified across N=3 + N=2 + N=1 +
// N=2 mix (kimi + qwen + glm + doubao).
func TestRegistry_AllFiveVendorsCoRegistered_NoCrossVendorShadowing(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID:   "https://adapter-deepseek.test:8080",
		QwenMaxModelID:    "https://adapter-qwen.test:8080",
		QwenPlusModelID:   "https://adapter-qwen.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
		GLMModelID:        "https://adapter-glm.test:8080",
		DoubaoProModelID:  "https://adapter-doubao.test:8080",
		DoubaoLiteModelID: "https://adapter-doubao.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hQwen, okQwen := reg.Resolve(QwenMaxModelID)
	hQwenPlus, _ := reg.Resolve(QwenPlusModelID)
	hKimi8k, okKimi8k := reg.Resolve(KimiV18kModelID)
	hKimi32k, _ := reg.Resolve(KimiV132kModelID)
	hKimi128k, _ := reg.Resolve(KimiV1128kModelID)
	hGLM, okGLM := reg.Resolve(GLMModelID)
	hDoubaoPro, okDP := reg.Resolve(DoubaoProModelID)
	hDoubaoLite, okDL := reg.Resolve(DoubaoLiteModelID)
	if !okDS || !okQwen || !okKimi8k || !okGLM || !okDP || !okDL {
		t.Fatalf("co-registered registry missing entries: okDS=%v okQwen=%v okKimi8k=%v okGLM=%v okDoubaoPro=%v okDoubaoLite=%v",
			okDS, okQwen, okKimi8k, okGLM, okDP, okDL)
	}
	// Story-4.2 N=2 invariant preserved.
	if hQwen != hQwenPlus {
		t.Fatalf("qwen-max and qwen-plus must share handle (Story-4.2 N=2 dedup preserved)")
	}
	// Story-4.3 N=3 invariant preserved.
	if hKimi8k != hKimi32k || hKimi32k != hKimi128k {
		t.Fatalf("kimi sizes must share handle (Story-4.3 N=3 dedup preserved)")
	}
	// Story-4.5 N=2 RESTORATION invariant.
	if hDoubaoPro != hDoubaoLite {
		t.Fatalf("doubao-pro and doubao-lite must share handle (Story-4.5 N=2 RESTORATION)")
	}
	// All cross-vendor pairs must use DISTINCT handles.
	if hDS == hQwen || hDS == hKimi8k || hDS == hGLM || hDS == hDoubaoPro ||
		hQwen == hKimi8k || hQwen == hGLM || hQwen == hDoubaoPro ||
		hKimi8k == hGLM || hKimi8k == hDoubaoPro ||
		hGLM == hDoubaoPro {
		t.Fatalf("cross-vendor handles must be DISTINCT; got hDS=%p hQwen=%p hKimi8k=%p hGLM=%p hDoubao=%p",
			hDS, hQwen, hKimi8k, hGLM, hDoubaoPro)
	}
}

// TestRegistry_UnknownDoubaoSibling_NotShadowed — regression test verifying
// that an obvious typo (`doubao-max` like qwen) does NOT accidentally
// resolve to the doubao handle (case-sensitive miss path).
func TestRegistry_UnknownDoubaoSibling_ReturnsOkFalse(t *testing.T) {
	const endpoint = "https://adapter-doubao.test:8080"
	reg := NewRegistry(map[string]string{
		DoubaoProModelID:  endpoint,
		DoubaoLiteModelID: endpoint,
	})
	for _, typo := range []string{"doubao-max", "doubao-plus", "Doubao-Pro", "DOUBAO-PRO"} {
		if _, ok := reg.Resolve(typo); ok {
			t.Fatalf("Resolve(%q) ok=true; expected miss (case-sensitive + no fuzzy match)", typo)
		}
	}
}

// 4.6-UNIT-011 (P0) — Architect Round 1 R9 anchor (Story-4.4 cascade):
// the Story-4.2 M2 `NewRegistry` byEndpoint endpoint-dedup branch
// handles the N=1 degenerate case CLEANLY for Story 4.6 as well —
// `ernie-4.0` maps to `ERNIE_ADAPTER_ENDPOINT`, the byEndpoint map has
// exactly one entry, the `if !ok { newConnectClientHandle }` branch
// executes once, and no `assert.Same` chain is triggered. SKIPPED-
// branch documentation test verbatim REUSE of 4.4-UNIT-011 pattern per
// Story-4.4 R9 ratification cascade.
func TestRegistry_Ernie40_M2_EndpointDedup_N1_NoOp_R9(t *testing.T) {
	const endpoint = "https://adapter-ernie.he-api-adapters.svc.cluster.local:8080"
	reg := NewRegistry(map[string]string{
		ErnieModelID: endpoint,
	})
	h, ok := reg.Resolve(ErnieModelID)
	if !ok {
		t.Fatalf("Resolve(ernie-4.0) ok=false, want true")
	}
	if h == nil {
		t.Fatalf("Resolve returned nil handle")
	}
	// SKIPPED-branch documentation: at N=1 the byEndpoint map has size 1,
	// the dedup branch fires once, and there is no second handle to
	// compare against. The Story-4.2 M2 mechanism is therefore a
	// well-behaved no-op for N=1 single-model-id vendors (RESTORATION
	// of Story-4.4 N=1 after Story-4.5 N=2).
	t.Logf("N=1 endpoint-dedup no-op verified — Resolve(ernie-4.0) returned a single handle without dedup work")
}

// 4.6-UNIT-012 (P0) — basic resolution path: Resolve(ernie-4.0) returns
// a non-nil handle; Resolve of a non-registered id returns (nil, false).
func TestRegistry_Ernie40_Resolve(t *testing.T) {
	reg := NewRegistry(map[string]string{
		ErnieModelID: "https://adapter-ernie.he-api-adapters.svc.cluster.local:8080",
	})
	if h, ok := reg.Resolve(ErnieModelID); !ok || h == nil {
		t.Fatalf("Resolve(ernie-4.0) returned (%v,%v); want (non-nil,true)", h, ok)
	}
	if h, ok := reg.Resolve("not-in-registry"); ok {
		t.Fatalf("Resolve(\"not-in-registry\") ok=true (h=%v), want false", h)
	}
}

// 4.6-UNIT-013 (P0) — OQ-4.6-2 ratification lock-in: env-var naming
// follows the adapter service BRAND (`ERNIE_*`), NOT the family name
// (`WENXIN_*`) or company name (`BAIDU_*`). Constants must be literal
// strings.
func TestRegistry_ErnieEndpointEnvName_IsLiteralBrandString(t *testing.T) {
	if ErnieAdapterEndpointEnv != "ERNIE_ADAPTER_ENDPOINT" {
		t.Fatalf("ErnieAdapterEndpointEnv = %q, want literal \"ERNIE_ADAPTER_ENDPOINT\" (OQ-4.6-2 brand-wins)", ErnieAdapterEndpointEnv)
	}
	if ErnieModelID != "ernie-4.0" {
		t.Fatalf("ErnieModelID = %q, want \"ernie-4.0\"", ErnieModelID)
	}
}

// 4.6-UNIT-014 — `LoadFromEnv` reads `ERNIE_ADAPTER_ENDPOINT` correctly +
// falls back gracefully on unset.
func TestRegistry_LoadFromEnv_ErnieEndpointSet_PopulatesErnie40(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	t.Setenv("ERNIE_ADAPTER_ENDPOINT", "https://adapter-ernie.test:8080")
	reg := LoadFromEnv()
	if h, ok := reg.Resolve("ernie-4.0"); !ok || h == nil {
		t.Fatalf("LoadFromEnv with ERNIE_ADAPTER_ENDPOINT must populate ernie-4.0: ok=%v h=%v", ok, h)
	}
	// Other vendors with empty endpoints stay omitted.
	if _, ok := reg.Resolve("deepseek-v3"); ok {
		t.Fatalf("empty DEEPSEEK_ADAPTER_ENDPOINT must not register deepseek-v3")
	}
}

func TestRegistry_LoadFromEnv_ErnieEndpointUnset_OmitsEntry(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	t.Setenv("ERNIE_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	if _, ok := reg.Resolve(ErnieModelID); ok {
		t.Fatalf("empty ERNIE_ADAPTER_ENDPOINT must not register ernie-4.0")
	}
}

// 4.6-INT-009 (P0) — SIX-vendor cross-vendor regression matrix (R8 +
// R9 — closes the Epic-4 vendor matrix): registering the ernie entry
// MUST NOT shadow Stories-4.1/4.2/4.3/4.4/4.5 entries. Each vendor's
// model ids continue to resolve to their own DISTINCT handle. M2 dedup
// invariant verified across N=3 (kimi) + N=2 (qwen) + N=1 (glm) + N=2
// (doubao) + N=1 (ernie) mix.
func TestRegistry_AllSixVendorsCoRegistered_NoCrossVendorShadowing(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID:   "https://adapter-deepseek.test:8080",
		QwenMaxModelID:    "https://adapter-qwen.test:8080",
		QwenPlusModelID:   "https://adapter-qwen.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
		GLMModelID:        "https://adapter-glm.test:8080",
		DoubaoProModelID:  "https://adapter-doubao.test:8080",
		DoubaoLiteModelID: "https://adapter-doubao.test:8080",
		ErnieModelID:      "https://adapter-ernie.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hQwen, okQwen := reg.Resolve(QwenMaxModelID)
	hQwenPlus, _ := reg.Resolve(QwenPlusModelID)
	hKimi8k, okKimi8k := reg.Resolve(KimiV18kModelID)
	hKimi32k, _ := reg.Resolve(KimiV132kModelID)
	hKimi128k, _ := reg.Resolve(KimiV1128kModelID)
	hGLM, okGLM := reg.Resolve(GLMModelID)
	hDoubaoPro, okDP := reg.Resolve(DoubaoProModelID)
	hDoubaoLite, okDL := reg.Resolve(DoubaoLiteModelID)
	hErnie, okErnie := reg.Resolve(ErnieModelID)
	if !okDS || !okQwen || !okKimi8k || !okGLM || !okDP || !okDL || !okErnie {
		t.Fatalf("co-registered registry missing entries: okDS=%v okQwen=%v okKimi8k=%v okGLM=%v okDoubaoPro=%v okDoubaoLite=%v okErnie=%v",
			okDS, okQwen, okKimi8k, okGLM, okDP, okDL, okErnie)
	}
	// Story-4.2 N=2 invariant preserved.
	if hQwen != hQwenPlus {
		t.Fatalf("qwen-max and qwen-plus must share handle (Story-4.2 N=2 dedup preserved)")
	}
	// Story-4.3 N=3 invariant preserved.
	if hKimi8k != hKimi32k || hKimi32k != hKimi128k {
		t.Fatalf("kimi sizes must share handle (Story-4.3 N=3 dedup preserved)")
	}
	// Story-4.5 N=2 RESTORATION invariant.
	if hDoubaoPro != hDoubaoLite {
		t.Fatalf("doubao-pro and doubao-lite must share handle (Story-4.5 N=2 RESTORATION)")
	}
	// All cross-vendor pairs (including the new ernie entry) must use
	// DISTINCT handles.
	if hDS == hQwen || hDS == hKimi8k || hDS == hGLM || hDS == hDoubaoPro || hDS == hErnie ||
		hQwen == hKimi8k || hQwen == hGLM || hQwen == hDoubaoPro || hQwen == hErnie ||
		hKimi8k == hGLM || hKimi8k == hDoubaoPro || hKimi8k == hErnie ||
		hGLM == hDoubaoPro || hGLM == hErnie ||
		hDoubaoPro == hErnie {
		t.Fatalf("cross-vendor handles must be DISTINCT; got hDS=%p hQwen=%p hKimi8k=%p hGLM=%p hDoubao=%p hErnie=%p",
			hDS, hQwen, hKimi8k, hGLM, hDoubaoPro, hErnie)
	}
}

// 4.6-INT-009-loadfromenv — SIX-vendor LoadFromEnv shape: every endpoint
// env-var set → each vendor's entries registered + their dedup invariants
// preserved + Ernie added.
func TestRegistry_LoadFromEnv_AllSixVendorsSet_PopulatesAllEntries(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "https://adapter-deepseek.test:8080")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "https://adapter-qwen.test:8080")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "https://adapter-kimi.test:8080")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "https://adapter-glm.test:8080")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "https://adapter-doubao.test:8080")
	t.Setenv("ERNIE_ADAPTER_ENDPOINT", "https://adapter-ernie.test:8080")
	reg := LoadFromEnv()
	for _, id := range []string{
		"deepseek-v3", "qwen-max", "qwen-plus",
		"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k",
		"glm-4", "doubao-pro", "doubao-lite", "ernie-4.0",
	} {
		if h, ok := reg.Resolve(id); !ok || h == nil {
			t.Fatalf("LoadFromEnv must populate %s: ok=%v h=%v", id, ok, h)
		}
	}
}

// 4.4-INT-009 (P0) — four-vendor cross-vendor regression: registering
// the GLM entry MUST NOT shadow Story-4.1 deepseek-v3, Story-4.2 qwen-*,
// OR Story-4.3 moonshot-v1-* entries. Each vendor's model ids continue
// to resolve to their own DISTINCT handle. R8 strengthened from N=3 to
// N=4 vendor families.
func TestRegistry_AllFourVendorsCoRegistered_NoCrossVendorShadowing(t *testing.T) {
	reg := NewRegistry(map[string]string{
		DeepSeekModelID:   "https://adapter-deepseek.test:8080",
		QwenMaxModelID:    "https://adapter-qwen.test:8080",
		QwenPlusModelID:   "https://adapter-qwen.test:8080",
		KimiV18kModelID:   "https://adapter-kimi.test:8080",
		KimiV132kModelID:  "https://adapter-kimi.test:8080",
		KimiV1128kModelID: "https://adapter-kimi.test:8080",
		GLMModelID:        "https://adapter-glm.test:8080",
	})
	hDS, okDS := reg.Resolve(DeepSeekModelID)
	hQwen, okQwen := reg.Resolve(QwenMaxModelID)
	hQwenPlus, _ := reg.Resolve(QwenPlusModelID)
	hKimi8k, okKimi8k := reg.Resolve(KimiV18kModelID)
	hKimi32k, _ := reg.Resolve(KimiV132kModelID)
	hKimi128k, _ := reg.Resolve(KimiV1128kModelID)
	hGLM, okGLM := reg.Resolve(GLMModelID)
	if !okDS || !okQwen || !okKimi8k || !okGLM {
		t.Fatalf("co-registered registry missing entries: okDS=%v okQwen=%v okKimi8k=%v okGLM=%v", okDS, okQwen, okKimi8k, okGLM)
	}
	// Story-4.2 N=2 invariant preserved.
	if hQwen != hQwenPlus {
		t.Fatalf("qwen-max and qwen-plus must share handle (Story-4.2 N=2 dedup preserved)")
	}
	// Story-4.3 N=3 invariant preserved.
	if hKimi8k != hKimi32k || hKimi32k != hKimi128k {
		t.Fatalf("kimi sizes must share handle (Story-4.3 N=3 dedup preserved)")
	}
	// All cross-vendor pairs must use DISTINCT handles.
	if hDS == hQwen || hDS == hKimi8k || hDS == hGLM ||
		hQwen == hKimi8k || hQwen == hGLM ||
		hKimi8k == hGLM {
		t.Fatalf("cross-vendor handles must be DISTINCT; got hDS=%p hQwen=%p hKimi8k=%p hGLM=%p", hDS, hQwen, hKimi8k, hGLM)
	}
}

// 9.5-UNIT-022 — the Vision ids ride the EXISTING services (BR-2.4): qwen now
// hosts N=3 (qwen-max/qwen-plus/qwen-vl-max) and glm N=2 (glm-4/glm-4v), each
// vendor's ids sharing ONE ClientHandle via M2 endpoint-dedup.
func TestRegistry_VisionIds_ShareVendorHandle_M2(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "https://adapter-qwen.test:8080")
	t.Setenv("KIMI_ADAPTER_ENDPOINT", "")
	t.Setenv("GLM_ADAPTER_ENDPOINT", "https://adapter-glm.test:8080")
	t.Setenv("DOUBAO_ADAPTER_ENDPOINT", "")
	t.Setenv("ERNIE_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()

	hMax, _ := reg.Resolve(QwenMaxModelID)
	hPlus, _ := reg.Resolve(QwenPlusModelID)
	hVL, okVL := reg.Resolve(QwenVLMaxModelID)
	if !okVL {
		t.Fatalf("qwen-vl-max must resolve via the qwen service")
	}
	if hMax != hPlus || hPlus != hVL {
		t.Fatalf("qwen N=3 must share ONE handle (M2): hMax=%p hPlus=%p hVL=%p", hMax, hPlus, hVL)
	}

	hGLM, _ := reg.Resolve(GLMModelID)
	hGLMV, okGLMV := reg.Resolve(GLM4VModelID)
	if !okGLMV {
		t.Fatalf("glm-4v must resolve via the glm service")
	}
	if hGLM != hGLMV {
		t.Fatalf("glm N=2 must share ONE handle (M2): hGLM=%p hGLMV=%p", hGLM, hGLMV)
	}
	// Cross-vendor handles stay distinct.
	if hVL == hGLMV {
		t.Fatalf("qwen-vl-max and glm-4v must live behind DISTINCT vendor handles")
	}
}
