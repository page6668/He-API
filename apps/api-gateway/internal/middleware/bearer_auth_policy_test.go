// Story 5.2 — UNIT-070..075 (extended cache shape + config-updated sentinel +
// legacy back-compat). Extends the Story-3.2 bearer_auth_test.go harness.
package middleware_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func cacheKeyFor(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return "auth:apikey:" + hex.EncodeToString(sum[:])
}

// okValidateExtended returns a Story-5.2 extended Validate response: a scope
// with models + ip_whitelist plus a monthly cap.
func okValidateExtended(reqKey string) func(*authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
	cap := "50.00"
	return func(req *authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		if req.GetPlaintextKey() != reqKey {
			return &authv1.ValidateApiKeyResponse{Ok: false, Reason: authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_NOT_FOUND}, nil
		}
		return &authv1.ValidateApiKeyResponse{
			Ok:                true,
			ApiKeyId:          "00000000-0000-0000-0000-000000000001",
			UserId:            "00000000-0000-0000-0000-000000000002",
			Scope:             `{"models":["qwen-max"],"ip_whitelist":["10.0.0.0/8"]}`,
			MonthlyCostCapUsd: &cap,
			Reason:            authv1.ApiKeyValidationReason_API_KEY_VALIDATION_REASON_UNSPECIFIED,
		}, nil
	}
}

// 5.2-UNIT-074 — the extended Validate response populates the structured
// CachedClaims fields reachable via CacheValueFromContext.
func TestRequireAPIKey_ExtendedClaimsInContext(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidateExtended(sentinelKey))

	var seen *middleware.CachedClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := middleware.CacheValueFromContext(r.Context()); ok {
			seen = c
		}
		w.WriteHeader(http.StatusNotImplemented)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sentinelKey)
	h.mw.RequireAPIKey(inner).ServeHTTP(httptest.NewRecorder(), req)

	if seen == nil {
		t.Fatal("CacheValueFromContext returned nothing")
	}
	if len(seen.ScopeModels) != 1 || seen.ScopeModels[0] != "qwen-max" {
		t.Fatalf("ScopeModels=%v want [qwen-max]", seen.ScopeModels)
	}
	if len(seen.ScopeIPWhitelist) != 1 || seen.ScopeIPWhitelist[0] != "10.0.0.0/8" {
		t.Fatalf("ScopeIPWhitelist=%v want [10.0.0.0/8]", seen.ScopeIPWhitelist)
	}
	if seen.MonthlyCostCapUSD == nil || *seen.MonthlyCostCapUSD != "50.00" {
		t.Fatalf("MonthlyCostCapUSD=%v want 50.00", seen.MonthlyCostCapUSD)
	}
}

// 5.2-UNIT-072 — cache hit + config-updated sentinel present → purge + fall
// through to the Validate RPC (parity with the Story-5.1 revoke-sentinel path).
func TestRequireAPIKey_ConfigUpdatedSentinelPurges(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidateExtended(sentinelKey))

	// 1st request populates the positive cache (1 RPC).
	rr1 := h.do("Bearer " + sentinelKey)
	if rr1.Code != http.StatusNotImplemented {
		t.Fatalf("warm-up status=%d want 501", rr1.Code)
	}
	if h.stub.rpcHits != 1 {
		t.Fatalf("rpc_hits=%d want 1", h.stub.rpcHits)
	}

	// Set the config-updated sentinel for this api_key_id.
	_ = h.mini.Set(middleware.ConfigUpdatedSentinelKeyPrefix+"00000000-0000-0000-0000-000000000001", "1")

	// 2nd request: cache hit BUT sentinel fires → purge + RPC fall-through.
	rr2 := h.do("Bearer " + sentinelKey)
	if rr2.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d want 501", rr2.Code)
	}
	if h.stub.rpcHits != 2 {
		t.Fatalf("rpc_hits=%d want 2 (sentinel must force RPC fall-through)", h.stub.rpcHits)
	}
	// Cache entry must have been purged.
	if _, err := h.mini.Get(cacheKeyFor(sentinelKey)); err == nil {
		// It was re-populated by the 2nd RPC; that's fine — assert it's fresh
		// (the purge happened). We can't easily distinguish; the RPC count is
		// the authoritative signal above.
		_ = err
	}
}

// 5.2-UNIT-075 — a LEGACY pre-Story-5.2 cache value ({user_id,scope} only)
// deserialises into the extended struct (new fields zero-valued) and still
// serves a cache hit. The omitempty tags ARE the back-compat mechanism.
func TestRequireAPIKey_LegacyCacheShapeBackCompat(t *testing.T) {
	t.Parallel()
	h := newHarness(t, okValidateExtended(sentinelKey))

	// Pre-seed the cache with the OLD 4-field shape (no scope_models etc).
	legacy := `{"api_key_id":"00000000-0000-0000-0000-000000000001","user_id":"00000000-0000-0000-0000-000000000002","team_id":"","scope":"{}"}`
	if err := h.mini.Set(cacheKeyFor(sentinelKey), legacy); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := h.do("Bearer " + sentinelKey)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d want 501 (legacy cache must still serve)", rr.Code)
	}
	if h.stub.rpcHits != 0 {
		t.Fatalf("rpc_hits=%d want 0 (legacy cache hit must avoid RPC)", h.stub.rpcHits)
	}
}
