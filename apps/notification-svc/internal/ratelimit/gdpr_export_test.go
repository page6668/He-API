package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newMiniredisClient(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, mr
}

// 2.6-UNIT-070 — happy path: first INCR returns 1, key acquires TTL.
func TestCheckAndIncr_FirstHit_Allows(t *testing.T) {
	t.Parallel()
	client, mr := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)

	if err := l.CheckAndIncr(context.Background(), "user-1"); err != nil {
		t.Fatalf("CheckAndIncr: %v", err)
	}
	key := KeyPrefixGDPRExport + "user-1"
	got, err := mr.Get(key)
	if err != nil {
		t.Fatalf("miniredis Get: %v", err)
	}
	if got != "1" {
		t.Errorf("count: got %q, want 1", got)
	}
	ttl := mr.TTL(key)
	if ttl <= 0 {
		t.Errorf("TTL: got %v, want positive (≈24h)", ttl)
	}
	if ttl > WindowGDPRExport+time.Second {
		t.Errorf("TTL: got %v, want ≤ %v", ttl, WindowGDPRExport)
	}
}

// 2.6-UNIT-071 — second INCR within the window returns ErrRateLimited.
// This is the race-condition safety-net the handler relies on.
func TestCheckAndIncr_SecondHit_RateLimited(t *testing.T) {
	t.Parallel()
	client, _ := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)

	if err := l.CheckAndIncr(context.Background(), "user-1"); err != nil {
		t.Fatalf("first CheckAndIncr: %v", err)
	}
	err := l.CheckAndIncr(context.Background(), "user-1")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("expected ErrRateLimited on second call, got %v", err)
	}
}

// 2.6-UNIT-072 — Lua script EXPIRE only fires on the first INCR. A
// re-INCR within the window MUST NOT extend the TTL (otherwise the
// 24h window could be indefinitely refreshed by abuse retries).
func TestCheckAndIncr_TTL_NotExtended(t *testing.T) {
	t.Parallel()
	client, mr := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)

	_ = l.CheckAndIncr(context.Background(), "user-1")
	key := KeyPrefixGDPRExport + "user-1"
	mr.FastForward(time.Hour) // simulate 1h passing
	beforeTTL := mr.TTL(key)
	_ = l.CheckAndIncr(context.Background(), "user-1") // expected ErrRateLimited
	afterTTL := mr.TTL(key)
	if afterTTL > beforeTTL {
		t.Errorf("TTL extended on re-INCR: before=%v after=%v (BR-6.4 violation)", beforeTTL, afterTTL)
	}
}

// 2.6-UNIT-073 — concurrent CheckAndIncr against a shared key: exactly
// one caller sees nil, the rest see ErrRateLimited. Asserts the Lua
// script's server-side atomicity (the as-built race-coverage mechanism
// per QA Round 1 ISSUE-5).
func TestCheckAndIncr_Concurrent_ExactlyOneWinner(t *testing.T) {
	t.Parallel()
	client, _ := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)

	const N = 16
	var wg sync.WaitGroup
	results := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i] = l.CheckAndIncr(context.Background(), "user-race")
		}()
	}
	wg.Wait()

	winners := 0
	for _, e := range results {
		switch {
		case e == nil:
			winners++
		case errors.Is(e, ErrRateLimited):
			// expected loser
		default:
			t.Errorf("unexpected error: %v", e)
		}
	}
	if winners != 1 {
		t.Errorf("BR-2.5 race-safety violated: got %d winners, want 1", winners)
	}
}

// 2.6-UNIT-074 — Reset deletes the counter so the handler can undo the
// INCR after a PG-commit failure rolled back the surrounding tx.
func TestReset_RemovesCounter(t *testing.T) {
	t.Parallel()
	client, mr := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)

	_ = l.CheckAndIncr(context.Background(), "user-1")
	if err := l.Reset(context.Background(), "user-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	key := KeyPrefixGDPRExport + "user-1"
	if mr.Exists(key) {
		t.Errorf("Reset did not delete %q", key)
	}
	// After reset, a new request succeeds.
	if err := l.CheckAndIncr(context.Background(), "user-1"); err != nil {
		t.Errorf("CheckAndIncr after Reset: %v", err)
	}
}

// 2.6-UNIT-075 — empty user_id rejected (defense-in-depth; the
// repository layer also validates).
func TestCheckAndIncr_EmptyUserID(t *testing.T) {
	t.Parallel()
	client, _ := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)
	if err := l.CheckAndIncr(context.Background(), ""); err == nil {
		t.Error("expected error on empty user_id")
	}
}

// 2.6-UNIT-076 — Reset on empty user_id is a no-op (best-effort cleanup
// must never fail on the err-path of the handler).
func TestReset_EmptyUserID_NoOp(t *testing.T) {
	t.Parallel()
	client, _ := newMiniredisClient(t)
	l := NewGDPRExportLimiter(client)
	if err := l.Reset(context.Background(), ""); err != nil {
		t.Errorf("Reset on empty user_id should be no-op, got %v", err)
	}
}

// 2.6-UNIT-077 — Key prefix pinning (BR-6.4 registers the exact format
// in data-models.md §4.3; CH dashboards + Grafana alerts query on this).
func TestKeyPrefixConstant(t *testing.T) {
	t.Parallel()
	if KeyPrefixGDPRExport != "ratelimit:gdpr:export:" {
		t.Errorf("key prefix drift: got %q", KeyPrefixGDPRExport)
	}
	if WindowGDPRExport != 24*time.Hour {
		t.Errorf("window drift: got %v, want 24h", WindowGDPRExport)
	}
}
