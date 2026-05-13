// oauth_ratelimit_test.go — Story 2.3 P6 ratelimit middleware tests.
//
// File→scenario mapping:
//   - 2.3-INT-019 (initiate 30/60s)
//   - 2.3-INT-020 (callback 30/60s)
//   - 2.3-INT-021 (Redis-down fail-CLOSED 503)
package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// Scenario: 2.3-INT-019
// 30 requests within the window pass; the 31st returns 429 with Retry-After.
func TestOAuthRatelimit_HitsLimit(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	rl := middleware.NewOAuthRatelimit(rdb)

	called := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Wrap("initiate", next)

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.4")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 30; i++ {
		rec := send()
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d: code = %d, want 200", i+1, rec.Code)
		}
	}
	rec := send()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("31st req: code = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429")
	}
	if called != 30 {
		t.Errorf("next called %d times, want exactly 30", called)
	}
}

// Scenario: 2.3-INT-020 — independent counters for initiate vs callback.
func TestOAuthRatelimit_SeparateCountersByEndpoint(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	rl := middleware.NewOAuthRatelimit(rdb)

	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	initiate := rl.Wrap("initiate", noop)
	callback := rl.Wrap("callback", noop)

	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		rec := httptest.NewRecorder()
		initiate.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("initiate %d: %d", i, rec.Code)
		}
	}
	// callback counter should be 0 — first request passes
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	rec := httptest.NewRecorder()
	callback.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("callback first request after maxed initiate: %d, want 200", rec.Code)
	}
}

// Scenario: 2.3-INT-021 + BR-4.2
// Redis unavailable → 503 fail-CLOSED (NOT 200, which would bypass).
func TestOAuthRatelimit_RedisDown_FailsClosed(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	rl := middleware.NewOAuthRatelimit(rdb)
	mr.Close()

	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := rl.Wrap("initiate", noop)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503 (fail-CLOSED)", rec.Code)
	}
}

// Scenario: 2.3-UNIT-060 — different IPs have independent counters.
func TestOAuthRatelimit_PerIP(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	rl := middleware.NewOAuthRatelimit(rdb)
	rl.Limit = 2 // tiny limit so the test is fast
	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := rl.Wrap("initiate", noop)

	send := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate", nil)
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	// IP A: 2 OK + 1 reject
	if send("198.51.100.1") != 200 {
		t.Fatal("A req1")
	}
	if send("198.51.100.1") != 200 {
		t.Fatal("A req2")
	}
	if send("198.51.100.1") != 429 {
		t.Fatal("A req3 should be 429")
	}
	// IP B: independent — 1 OK
	if send("198.51.100.2") != 200 {
		t.Fatal("B req1 should be 200 (independent IP counter)")
	}
}

// Sanity — TTL on key matches the window.
func TestOAuthRatelimit_KeyTTL(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	rl := middleware.NewOAuthRatelimit(rdb)
	rl.Window = 30 * time.Second

	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := rl.Wrap("initiate", noop)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oauth/google/initiate", nil)
	req.Header.Set("X-Forwarded-For", "192.0.2.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("first req: %d", rec.Code)
	}
	ttl := mr.TTL("ratelimit:oauth:initiate:ip:192.0.2.1")
	if ttl == 0 || ttl > 30*time.Second {
		t.Errorf("TTL = %s, want ≈30s", ttl)
	}
}
