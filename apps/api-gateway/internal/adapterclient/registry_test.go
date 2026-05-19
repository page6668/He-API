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
	reg := LoadFromEnv()
	if h, ok := reg.Resolve("deepseek-v3"); !ok || h == nil {
		t.Fatalf("env-loaded registry missing deepseek-v3 entry: ok=%v h=%v", ok, h)
	}
}

func TestRegistry_LoadFromEnv_EmptyEnv_OmitsEntry(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	t.Setenv("QWEN_ADAPTER_ENDPOINT", "")
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
