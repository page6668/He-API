// Story 6.5 — white-box coverage for the routing-pref write-through edge paths
// (nil reader + Redis error) that the handler-level tests can't reach (the
// handler only invokes it with a live s.Redis).
package handlers

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestWriteThroughRoutingPref_NilAndEmpty_NoOp(t *testing.T) {
	t.Parallel()
	// nil client → no-op, no error.
	if err := writeThroughRoutingPref(context.Background(), nil, "u1", nil); err != nil {
		t.Fatalf("nil rdb: %v", err)
	}
	// empty userID → no-op even with a live client.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := writeThroughRoutingPref(context.Background(), rdb, "", nil); err != nil {
		t.Fatalf("empty userID: %v", err)
	}
}

func TestWriteThroughRoutingPref_RedisError_Propagates(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close() // every command now errors
	v := "cost"
	if err := writeThroughRoutingPref(context.Background(), rdb, "u1", &v); err == nil {
		t.Fatalf("expected a Redis error to propagate (caller logs WARN, never blocks the 200)")
	}
}
