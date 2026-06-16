// Story 6.5 — gateway hot-path resolver tests (Q-A Option B: cache + sentinel).
package userpref

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

const uid = "11111111-1111-1111-1111-111111111111"

// countingCache wraps a real client and counts GET calls (UNIT-023 budget).
type countingCache struct {
	Cache
	gets atomic.Int64
}

func (c *countingCache) Get(ctx context.Context, key string) *redis.StringCmd {
	c.gets.Add(1)
	return c.Cache.Get(ctx, key)
}

// fakeMe is a MeFetcher that counts RPCs and returns a fixed value/err.
type fakeMe struct {
	calls atomic.Int64
	drs   *string
	err   error
}

func (f *fakeMe) GetMe(_ context.Context, _ *connect.Request[authv1.GetMeRequest]) (*connect.Response[authv1.GetMeResponse], error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&authv1.GetMeResponse{UserId: uid, DefaultRoutingStrategy: f.drs}), nil
}

func newClient(t *testing.T) (*countingCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &countingCache{Cache: rdb}, mr
}

func strp(s string) *string { return &s }

// 6.5-UNIT-023 — cache HIT returns the strategy via a single GET, zero RPC.
func TestResolve_CacheHit_SingleGetZeroRPC(t *testing.T) {
	t.Parallel()
	c, mr := newClient(t)
	_ = mr.Set(RoutingPrefKey(uid), "cost")
	me := &fakeMe{}
	r := NewResolver(c, me, nil)

	got := r.ResolveUserDefault(context.Background(), uid)
	if got != routingv1.Strategy_STRATEGY_COST {
		t.Errorf("strategy = %v, want COST", got)
	}
	if c.gets.Load() != 1 {
		t.Errorf("GET count = %d, want 1 (hot-path budget)", c.gets.Load())
	}
	if me.calls.Load() != 0 {
		t.Errorf("auth-svc RPC count = %d, want 0 on a cache hit", me.calls.Load())
	}
}

// 6.5-UNIT-024 — cache MISS lazy-populates via GetMe and SETEX-caches the value.
func TestResolve_CacheMiss_LazyPopulate(t *testing.T) {
	t.Parallel()
	c, mr := newClient(t)
	me := &fakeMe{drs: strp("latency")}
	r := NewResolver(c, me, nil)

	got := r.ResolveUserDefault(context.Background(), uid)
	if got != routingv1.Strategy_STRATEGY_LATENCY {
		t.Errorf("strategy = %v, want LATENCY", got)
	}
	if me.calls.Load() != 1 {
		t.Errorf("RPC count = %d, want 1 (lazy-populate)", me.calls.Load())
	}
	if v, _ := mr.Get(RoutingPrefKey(uid)); v != "latency" {
		t.Errorf("cache after populate = %q, want latency", v)
	}
}

// 6.5-UNIT-025 / BLIND-ERROR-001 — miss + GetMe error → fail-OPEN UNSPECIFIED.
func TestResolve_MissGetMeError_FailOpen(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t)
	me := &fakeMe{err: errors.New("auth-svc down")}
	r := NewResolver(c, me, nil)

	if got := r.ResolveUserDefault(context.Background(), uid); got != routingv1.Strategy_STRATEGY_UNSPECIFIED {
		t.Errorf("strategy = %v, want UNSPECIFIED (fail-open)", got)
	}
}

// 6.5-UNIT-026 / BLIND-ERROR-002 — Redis GET error (non-Nil) → fail-OPEN; no RPC.
func TestResolve_RedisError_FailOpen(t *testing.T) {
	t.Parallel()
	c, mr := newClient(t)
	mr.Close() // every command now errors (connection refused)
	me := &fakeMe{drs: strp("cost")}
	r := NewResolver(c, me, nil)

	if got := r.ResolveUserDefault(context.Background(), uid); got != routingv1.Strategy_STRATEGY_UNSPECIFIED {
		t.Errorf("strategy = %v, want UNSPECIFIED on Redis error", got)
	}
	if me.calls.Load() != 0 {
		t.Errorf("a non-Nil Redis error must NOT trigger a lazy GetMe; RPC count = %d", me.calls.Load())
	}
}

// 6.5-UNIT-027 / INT-007 — sentinel EXISTS on a hit → re-resolve; stale unused.
func TestResolve_SentinelExists_ReResolves(t *testing.T) {
	t.Parallel()
	c, mr := newClient(t)
	_ = mr.Set(RoutingPrefKey(uid), "cost")        // stale cached value
	_ = mr.Set(PrefUpdatedKey(uid), "1")           // a save just happened
	me := &fakeMe{drs: strp("quality")}            // authoritative new value
	r := NewResolver(c, me, nil)

	got := r.ResolveUserDefault(context.Background(), uid)
	if got != routingv1.Strategy_STRATEGY_QUALITY {
		t.Errorf("strategy = %v, want QUALITY (re-resolved, stale 'cost' NOT used)", got)
	}
	if me.calls.Load() != 1 {
		t.Errorf("sentinel must force exactly one re-resolve RPC; got %d", me.calls.Load())
	}
	// Sentinel consumed; the fresh value is now cached.
	if mr.Exists(PrefUpdatedKey(uid)) {
		t.Errorf("sentinel should be deleted after consumption")
	}
	if v, _ := mr.Get(RoutingPrefKey(uid)); v != "quality" {
		t.Errorf("cache after re-resolve = %q, want quality", v)
	}
}

// 6.5-UNIT-028 / BLIND-BOUNDARY-001 — empty userID → UNSPECIFIED, no work.
func TestResolve_EmptyUserID_NoResolution(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t)
	me := &fakeMe{drs: strp("cost")}
	r := NewResolver(c, me, nil)

	if got := r.ResolveUserDefault(context.Background(), ""); got != routingv1.Strategy_STRATEGY_UNSPECIFIED {
		t.Errorf("strategy = %v, want UNSPECIFIED for empty userID", got)
	}
	if c.gets.Load() != 0 || me.calls.Load() != 0 {
		t.Errorf("empty userID must do no work; gets=%d rpc=%d", c.gets.Load(), me.calls.Load())
	}
}

// "no default" persisted ("") → UNSPECIFIED (tier skipped → STRATEGY_DEFAULT).
func TestResolve_NoDefault_Unspecified(t *testing.T) {
	t.Parallel()
	c, mr := newClient(t)
	_ = mr.Set(RoutingPrefKey(uid), "")
	r := NewResolver(c, &fakeMe{}, nil)

	if got := r.ResolveUserDefault(context.Background(), uid); got != routingv1.Strategy_STRATEGY_UNSPECIFIED {
		t.Errorf("strategy = %v, want UNSPECIFIED for empty persisted value", got)
	}
}
