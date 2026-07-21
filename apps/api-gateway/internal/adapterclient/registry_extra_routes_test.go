package adapterclient

import "testing"

// HE_API_EXTRA_MODEL_ROUTES lets an operator route newly released vendor models
// without a redeploy. Regression guard for the 2026-07 catalogue rot: every
// compiled-in id (qwen-max, deepseek-v3, …) had been retired upstream, so live
// calls returned 403 Model.AccessDenied.
func TestLoadFromEnv_ExtraModelRoutes(t *testing.T) {
	t.Setenv(QwenAdapterEndpointEnv, "http://qwen:8080")
	t.Setenv(DeepSeekEndpointEnv, "http://deepseek:8080")
	t.Setenv(ExtraModelRoutesEnv, "qwen3.7-max=qwen, qwen3.7-plus=qwen,deepseek-v4-pro=deepseek")

	r := LoadFromEnv()
	for _, id := range []string{"qwen3.7-max", "qwen3.7-plus", "deepseek-v4-pro"} {
		if _, ok := r.Resolve(id); !ok {
			t.Errorf("Resolve(%q) = miss, want hit", id)
		}
	}
	// The built-in ids keep resolving (additive, not a replacement).
	if _, ok := r.Resolve(QwenMaxModelID); !ok {
		t.Error("built-in qwen-max should still resolve")
	}
}

// A typo must never take the gateway down: malformed pairs, unknown vendors and
// vendors whose adapter endpoint is unset are skipped, not fatal.
func TestLoadFromEnv_ExtraModelRoutes_IgnoresBadInput(t *testing.T) {
	t.Setenv(QwenAdapterEndpointEnv, "http://qwen:8080")
	t.Setenv(KimiAdapterEndpointEnv, "") // vendor wired in the map but unset
	t.Setenv(ExtraModelRoutesEnv, "no-equals-sign,ghost=nosuchvendor,orphan=kimi,=qwen, good=qwen")

	r := LoadFromEnv()
	for _, id := range []string{"no-equals-sign", "ghost", "orphan", ""} {
		if _, ok := r.Resolve(id); ok {
			t.Errorf("Resolve(%q) = hit, want miss", id)
		}
	}
	if _, ok := r.Resolve("good"); !ok {
		t.Error(`Resolve("good") = miss, want hit`)
	}
}
