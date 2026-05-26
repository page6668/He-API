// Story 5.3 — unit tests for the ratelimit Middleware (AC1 QPS + AC2 RPM
// + AC3 TPM pre-check). Backed by alicebob/miniredis/v2 — in-process
// Redis 7.x compatible stub honouring EVAL/EVALSHA + EXPIRE NX. Test
// IDs follow the 5.3-UNIT-NNN convention from
// docs/qa/assessments/5.3-test-design-20260526.md.

package ratelimit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
)

// ----- Test helpers ------------------------------------------------------

// newTestMiddleware spins a miniredis server, a real go-redis client
// connected to it, and a Middleware with the supplied ceilings. Caller
// MUST defer the returned cleanup.
func newTestMiddleware(t *testing.T, ceilings ratelimit.Ceilings) (*miniredis.Miniredis, *redis.Client, *ratelimit.Middleware, *bytes.Buffer, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m := ratelimit.New(ratelimit.Config{
		Redis:            client,
		FreeTierDefaults: ceilings,
		// generous timeout so CI noise doesn't trigger fail-open.
		FailOpenTimeout: 200 * time.Millisecond,
	}, logger)

	cleanup := func() {
		_ = client.Close()
		mr.Close()
	}
	return mr, client, m, &buf, cleanup
}

// mrGet wraps miniredis.Get into a single-return form. miniredis returns
// ("", ErrKeyNotFound) on missing keys; our tests treat missing as "".
func mrGet(t *testing.T, mr *miniredis.Miniredis, key string) string {
	t.Helper()
	v, err := mr.Get(key)
	if err != nil {
		return ""
	}
	return v
}

// passHandler is the inner handler the middleware delegates to on allowed
// requests. Records its invocation count via the supplied atomic.
func passHandler(called *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// withAPIKeyID returns a request whose context carries the supplied
// api_key_id (mirroring what bearer-auth populates at line 357 of
// bearer_auth.go via middleware.WithAPIKeyID).
func withAPIKeyID(apiKeyID string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return r.WithContext(middleware.WithAPIKeyID(r.Context(), apiKeyID))
}

// ----- AC1 QPS tests -----------------------------------------------------

// 5.3-UNIT-001 — counter at qps_max returns 429 + Retry-After: 1.
func TestUNIT001_QPSAtCeilingReturns429(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 10, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-001"
	// Pre-seed the counter to the ceiling.
	mr.Set("ratelimit:key:"+apiKeyID+":qps", "10")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":qps", 1*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After: want %q, got %q", "1", got)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "429_rate_limit_qps" {
		t.Errorf("envelope code: want 429_rate_limit_qps, got %q", env.Error.Code)
	}
	if called.Load() != 0 {
		t.Errorf("downstream handler should not have run on 429")
	}
}

// 5.3-UNIT-002 — counter below qps_max allowed; counter increments.
func TestUNIT002_QPSBelowCeilingAllowed(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 10, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-002"
	mr.Set("ratelimit:key:"+apiKeyID+":qps", "5")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":qps", 1*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if called.Load() != 1 {
		t.Errorf("downstream handler should have run exactly once, got %d", called.Load())
	}
	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":qps"); got != "6" {
		t.Errorf("QPS counter: want 6, got %q", got)
	}
}

// 5.3-UNIT-004 — slog discipline: NO plaintext API key in record.
func TestUNIT004_SlogNoPlaintextKey(t *testing.T) {
	mr, _, m, buf, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 1, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-004-uuid-shape"
	// Run two requests: 2nd should hit the ceiling and emit a denied slog.
	mr.Set("ratelimit:key:"+apiKeyID+":qps", "1")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":qps", 1*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}

	// Plaintext API key shape is `he-<≥10-char base62>`. Verify the log
	// buffer carries NEITHER a plaintext shape NOR any message content.
	out := buf.String()
	if regexp.MustCompile(`he-[A-Za-z0-9]{10,}`).MatchString(out) {
		t.Errorf("slog contains plaintext-key-shape match: %s", out)
	}
	if regexp.MustCompile(`"messages":\s*\[`).MatchString(out) {
		t.Errorf("slog contains messages content: %s", out)
	}
}

// 5.3-UNIT-005 — atomicity: 100 goroutines vs qps_max=10 → exactly 10 allowed.
func TestUNIT005_QPSAtomicity100Goroutines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race-heavy test in -short mode")
	}
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 10, RPMMax: 1000, TPMMax: 1_000_000})
	defer cleanup()
	_ = mr

	apiKeyID := "k-005"
	const total = 100

	var (
		called atomic.Int32
		ok     atomic.Int32
		denied atomic.Int32
		wg     sync.WaitGroup
	)
	handler := m.Wrap(passHandler(&called))
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, withAPIKeyID(apiKeyID))
			switch rec.Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := ok.Load(); got != 10 {
		t.Errorf("allowed: want 10, got %d", got)
	}
	if got := denied.Load(); got != 90 {
		t.Errorf("denied: want 90, got %d", got)
	}
	if got := called.Load(); got != 10 {
		t.Errorf("downstream invocations: want 10, got %d", got)
	}
}

// 5.3-UNIT-006 — TTL NX semantics: 2nd INCR within same window does NOT
// extend TTL.
func TestUNIT006_QPSNXTTLSemantics(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 10, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-006"
	var called atomic.Int32
	handler := m.Wrap(passHandler(&called))

	// First request — establishes counter + sets TTL = 1s.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, withAPIKeyID(apiKeyID))
	ttl1 := mr.TTL("ratelimit:key:" + apiKeyID + ":qps")

	// Advance simulated clock by 0.4s — TTL should drop to ~0.6s.
	mr.FastForward(400 * time.Millisecond)
	ttl2 := mr.TTL("ratelimit:key:" + apiKeyID + ":qps")
	if ttl2 >= ttl1 {
		t.Errorf("TTL did not decrease after FastForward: ttl1=%v ttl2=%v", ttl1, ttl2)
	}

	// Second request — must INCR but NOT extend TTL (EXPIRE NX).
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, withAPIKeyID(apiKeyID))
	ttl3 := mr.TTL("ratelimit:key:" + apiKeyID + ":qps")
	if ttl3 > ttl2 {
		t.Errorf("EXPIRE NX violated — TTL extended after 2nd INCR: ttl2=%v ttl3=%v", ttl2, ttl3)
	}
}

// 5.3-UNIT-007 — cross-axis short-circuit: QPS exhausted → RPM/TPM NOT
// incremented (BR-2.4 atomicity).
func TestUNIT007_CrossAxisShortCircuit(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 1, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-007"
	mr.Set("ratelimit:key:"+apiKeyID+":qps", "1")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":qps", 1*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}

	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":rpm"); got != "" {
		t.Errorf("RPM counter should not exist on QPS denial, got %q", got)
	}
	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm"); got != "" {
		t.Errorf("TPM counter should not exist on QPS denial, got %q", got)
	}
}

// ----- AC2 RPM tests -----------------------------------------------------

// 5.3-UNIT-008 — counter at rpm_max returns 429_rate_limit_rpm.
func TestUNIT008_RPMAtCeilingReturns429(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 100, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-008"
	if err := mr.Set("ratelimit:key:"+apiKeyID+":rpm", "300"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	mr.SetTTL("ratelimit:key:"+apiKeyID+":rpm", 45*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "429_rate_limit_rpm" {
		t.Errorf("envelope code: want 429_rate_limit_rpm, got %q", env.Error.Code)
	}
	retry, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	if retry < 1 || retry > 60 {
		t.Errorf("Retry-After: want 1-60 inclusive, got %d", retry)
	}
}

// 5.3-UNIT-009 — Retry-After equals Redis TTL remaining (within ±1s).
func TestUNIT009_RPMRetryAfterEqualsTTL(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 100, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-009"
	mr.Set("ratelimit:key:"+apiKeyID+":rpm", "300")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":rpm", 18*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	retry, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	// Allow ±1s drift due to clock granularity inside miniredis.
	if retry < 17 || retry > 19 {
		t.Errorf("Retry-After: want 17-19, got %d", retry)
	}
}

// 5.3-UNIT-011 — RPM atomicity: 5 goroutines vs counter=297, rpm_max=300
// → exactly 3 allowed / 2 denied.
func TestUNIT011_RPMAtomicityNearCeiling(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 100, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-011"
	mr.Set("ratelimit:key:"+apiKeyID+":rpm", "297")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":rpm", 60*time.Second)

	const total = 5
	var (
		ok     atomic.Int32
		denied atomic.Int32
		wg     sync.WaitGroup
	)
	var called atomic.Int32
	handler := m.Wrap(passHandler(&called))
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, withAPIKeyID(apiKeyID))
			switch rec.Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusTooManyRequests:
				denied.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := ok.Load(); got != 3 {
		t.Errorf("allowed: want 3, got %d", got)
	}
	if got := denied.Load(); got != 2 {
		t.Errorf("denied: want 2, got %d", got)
	}
}

// ----- AC3 TPM tests -----------------------------------------------------

// 5.3-UNIT-013 — pre-check counter at tpm_max returns 429_rate_limit_tpm.
func TestUNIT013_TPMAtCeilingReturns429(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 100, RPMMax: 1000, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-013"
	mr.Set("ratelimit:key:"+apiKeyID+":tpm", "60000")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":tpm", 30*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rec.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "429_rate_limit_tpm" {
		t.Errorf("envelope code: want 429_rate_limit_tpm, got %q", env.Error.Code)
	}
	retry, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	if retry < 1 || retry > 60 {
		t.Errorf("Retry-After: want 1-60, got %d", retry)
	}
}

// 5.3-UNIT-014 — pre-check passes but does NOT increment TPM counter
// (post-deduction model — BR-3.4).
func TestUNIT014_TPMPreCheckNoIncrement(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 100, RPMMax: 1000, TPMMax: 60000})
	defer cleanup()

	apiKeyID := "k-014"
	mr.Set("ratelimit:key:"+apiKeyID+":tpm", "45000")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":tpm", 30*time.Second)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID(apiKeyID))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// TPM counter must NOT have moved (post-deduction lands later).
	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm"); got != "45000" {
		t.Errorf("TPM counter should be unchanged on pre-check, want 45000, got %q", got)
	}
}

// ----- Cross-cutting tests ----------------------------------------------

// 5.3-SEC-003 — `api_key_id` missing from context returns 500
// `500_gateway_misconfigured` (defensive).
func TestSEC003_MissingAPIKeyIDReturns500(t *testing.T) {
	_, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 10, RPMMax: 300, TPMMax: 60000})
	defer cleanup()

	var called atomic.Int32
	rec := httptest.NewRecorder()
	// Plain request with no api_key_id in context.
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	m.Wrap(passHandler(&called)).ServeHTTP(rec, r)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	if called.Load() != 0 {
		t.Errorf("downstream handler must not run on 500")
	}
}

// 5.3-INT-004-lite — Redis nil → fail-OPEN (pass-through).
func TestRedisNilFailsOpen(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	m := ratelimit.New(ratelimit.Config{
		Redis:            nil,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 1, RPMMax: 1, TPMMax: 1},
		FailOpenTimeout:  100 * time.Millisecond,
	}, logger)

	var called atomic.Int32
	rec := httptest.NewRecorder()
	m.Wrap(passHandler(&called)).ServeHTTP(rec, withAPIKeyID("k-fail-open"))

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open should pass through, got %d", rec.Code)
	}
	if called.Load() != 1 {
		t.Errorf("downstream handler should run on fail-open, called=%d", called.Load())
	}
}

// 5.3-SEC-002 — per-key isolation: key_A burns budget, key_B unaffected.
func TestSEC002_PerKeyIsolation(t *testing.T) {
	mr, _, m, _, cleanup := newTestMiddleware(t, ratelimit.Ceilings{QPSMax: 1, RPMMax: 100, TPMMax: 60000})
	defer cleanup()
	_ = mr

	var called atomic.Int32
	handler := m.Wrap(passHandler(&called))

	// Burn key A's QPS budget.
	recA1 := httptest.NewRecorder()
	handler.ServeHTTP(recA1, withAPIKeyID("k-A"))
	if recA1.Code != http.StatusOK {
		t.Fatalf("A request 1: want 200, got %d", recA1.Code)
	}
	recA2 := httptest.NewRecorder()
	handler.ServeHTTP(recA2, withAPIKeyID("k-A"))
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("A request 2: want 429, got %d", recA2.Code)
	}

	// Key B should still pass.
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, withAPIKeyID("k-B"))
	if recB.Code != http.StatusOK {
		t.Errorf("key B unaffected isolation broken: got %d", recB.Code)
	}
}

// Ensure the global logger doesn't write anywhere disruptive during tests.
func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	_ = context.Background()
}
