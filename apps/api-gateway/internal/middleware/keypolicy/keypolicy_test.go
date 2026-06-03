// Story 5.2 — UNIT-030..045 (keypolicy middleware: gates + ordering + fail-open).
package keypolicy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
)

func capPtr(s string) *string { return &s }

// run drives the middleware with the given claims + request body, returning
// the recorder + whether the downstream handler ran.
func run(t *testing.T, claims *middleware.CachedClaims, opts keypolicy.Options, body string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})
	h := keypolicy.New(opts)(next)

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.RemoteAddr = "10.0.0.5:1111"
	ctx := middleware.WithCacheValue(context.Background(), claims)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(ctx))
	return w, nextCalled
}

func TestKeyPolicy_IPWhitelist(t *testing.T) {
	t.Run("deny → 403 ip_not_whitelisted, short-circuit", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", UserID: "u1", ScopeIPWhitelist: []string{"192.168.1.0/24"}}
		w, next := run(t, claims, keypolicy.Options{}, `{"model":"qwen-max"}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403", w.Code)
		}
		if !strings.Contains(w.Body.String(), "403_ip_not_whitelisted") {
			t.Fatalf("body=%s", w.Body.String())
		}
		if next {
			t.Fatalf("handler ran on deny — must short-circuit")
		}
	})

	t.Run("allow (IP in whitelist) → passthrough", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", ScopeIPWhitelist: []string{"10.0.0.0/8"}}
		_, next := run(t, claims, keypolicy.Options{}, `{"model":"qwen-max"}`)
		if !next {
			t.Fatalf("handler must run when IP allowed")
		}
	})
}

func TestKeyPolicy_ModelScope(t *testing.T) {
	t.Run("deny → 403 model_not_in_scope", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", ScopeModels: []string{"qwen-max"}}
		w, next := run(t, claims, keypolicy.Options{}, `{"model":"deepseek-v3"}`)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "403_model_not_in_scope") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if next {
			t.Fatalf("must short-circuit")
		}
	})

	t.Run("allow → passthrough", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", ScopeModels: []string{"qwen-max"}}
		_, next := run(t, claims, keypolicy.Options{}, `{"model":"qwen-max"}`)
		if !next {
			t.Fatalf("handler must run")
		}
	})
}

func TestKeyPolicy_MonthlyCap(t *testing.T) {
	reader := func(cur string, err error) keypolicy.MonthlyCostReader {
		return func(context.Context, string) (string, bool, error) { return cur, cur != "", err }
	}

	t.Run("over cap → 402", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", MonthlyCostCapUSD: capPtr("50.00")}
		w, next := run(t, claims, keypolicy.Options{CostReader: reader("50.01", nil)}, `{"model":"qwen-max"}`)
		if w.Code != http.StatusPaymentRequired || !strings.Contains(w.Body.String(), "402_quota_exhausted") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if next {
			t.Fatalf("must short-circuit")
		}
	})

	t.Run("under cap → passthrough", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", MonthlyCostCapUSD: capPtr("50.00")}
		_, next := run(t, claims, keypolicy.Options{CostReader: reader("25.00", nil)}, `{"model":"qwen-max"}`)
		if !next {
			t.Fatalf("handler must run")
		}
	})

	t.Run("Redis error → fail-OPEN passthrough (Q-F/BR-4.6)", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1", MonthlyCostCapUSD: capPtr("50.00")}
		_, next := run(t, claims, keypolicy.Options{CostReader: reader("", errors.New("redis down"))}, `{"model":"qwen-max"}`)
		if !next {
			t.Fatalf("fail-open: handler must run on Redis error")
		}
	})

	t.Run("nil cap → cap check skipped", func(t *testing.T) {
		claims := &middleware.CachedClaims{APIKeyID: "k1"}
		_, next := run(t, claims, keypolicy.Options{}, `{"model":"qwen-max"}`)
		if !next {
			t.Fatalf("no cap → passthrough")
		}
	})
}

func TestKeyPolicy_Ordering(t *testing.T) {
	// IP gate fires BEFORE model gate — even when BOTH would deny, the
	// response is the IP envelope (first-denial-wins short-circuit).
	claims := &middleware.CachedClaims{
		APIKeyID:         "k1",
		ScopeIPWhitelist: []string{"192.168.1.0/24"}, // client 10.0.0.5 denied
		ScopeModels:      []string{"qwen-max"},       // model deepseek-v3 also denied
	}
	w, _ := run(t, claims, keypolicy.Options{}, `{"model":"deepseek-v3"}`)
	if !strings.Contains(w.Body.String(), "403_ip_not_whitelisted") {
		t.Fatalf("IP gate must fire first; body=%s", w.Body.String())
	}
}

func TestKeyPolicy_NoClaims_Passthrough(t *testing.T) {
	next := false
	h := keypolicy.New(keypolicy.Options{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { next = true }))
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r) // no cache value in ctx
	if !next {
		t.Fatalf("missing claims → defensive passthrough")
	}
}
