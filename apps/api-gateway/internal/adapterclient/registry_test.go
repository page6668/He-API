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
	if h, ok := reg.Resolve("qwen-max"); ok {
		t.Fatalf("Resolve(qwen-max) ok=true (h=%v), want false (fall through to mock)", h)
	}
	if _, ok := reg.Resolve(""); ok {
		t.Fatalf("Resolve(\"\") ok=true, want false")
	}
}

func TestRegistry_LoadFromEnv_PicksUpDeepseekEndpointWhenSet(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "https://adapter-deepseek.test:8080")
	reg := LoadFromEnv()
	if h, ok := reg.Resolve("deepseek-v3"); !ok || h == nil {
		t.Fatalf("env-loaded registry missing deepseek-v3 entry: ok=%v h=%v", ok, h)
	}
}

func TestRegistry_LoadFromEnv_EmptyEnv_OmitsEntry(t *testing.T) {
	t.Setenv("DEEPSEEK_ADAPTER_ENDPOINT", "")
	reg := LoadFromEnv()
	if _, ok := reg.Resolve("deepseek-v3"); ok {
		t.Fatalf("empty DEEPSEEK_ADAPTER_ENDPOINT should result in no deepseek-v3 registration")
	}
}

// 4.1-BLIND-BOUNDARY-003 (P1) — QA Round 1 fix. Uppercase / mixed-case
// model ids do NOT resolve (registry keys are case-sensitive, parity with
// the Story 3.5 BR-1.4 `^[a-z0-9][a-z0-9.\-]*$` regex). The case-sensitive
// miss preserves the mock fall-through path; Stories 4.2-4.6 inherit the
// same guard.
func TestRegistry_Resolve_UppercaseModelId_MissesRegistry(t *testing.T) {
	reg := NewRegistry(map[string]string{"deepseek-v3": "https://x"})
	for _, id := range []string{"DeepSeek-V3", "DEEPSEEK-V3", "Deepseek-V3"} {
		if _, ok := reg.Resolve(id); ok {
			t.Fatalf("Resolve(%q) ok=true; expected case-sensitive miss", id)
		}
	}
}
