// Story 5.4 AC3 — PurgeMonthlyState unit coverage (QA Round 1 ISSUE-003).
//
// ISSUE-003 (medium): the cluster-aware cron purge helper had ZERO test
// files. A mis-purge resets breakers mid-month (cost-control compromise), so
// the type-switch dispatch + per-prefix glob is security-sensitive. These
// tests exercise the *standalone* (*redis.Client) branch of purgePattern's
// type switch via miniredis — the cluster (*redis.ClusterClient /
// ForEachMaster) branch is covered by 5.4-INT-029 (3-node cluster,
// testcontainers; deferred — see apps/api-gateway/tests/5_4_cap_breaker_skeleton_test.go).
//
// The decisive invariant under test is GLOB ISOLATION: the sticky-sentinel
// glob `keystate:apikey:cap_tripped:*` MUST NOT swallow the dedupe family
// `keystate:apikey:cap_tripped_notified:*`, and the dedupe glob
// `keystate:apikey:cap_*_notified:*` MUST cover BOTH dedupe families while
// leaving every unrelated key (auth:apikey:revoked:*, foreign usage keys)
// untouched. A glob over-reach here would silently corrupt unrelated state.
package redisclient_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/auth-svc/internal/redisclient"
)

func newStandaloneRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// Scenario: 5.4-UNIT-051 (standalone dispatch) + 5.4-INT-017/018/019 (glob — unit realisation)
// PurgeMonthlyState on a *redis.Client (standalone — the non-ClusterClient
// arm of the type switch) deletes exactly the three cap-state families and
// reports accurate per-family counts. Unrelated keys survive.
func TestPurgeMonthlyState_StandalonePurgesAllThreeFamilies(t *testing.T) {
	t.Parallel()
	rdb, mr := newStandaloneRedis(t)
	ctx := context.Background()

	const (
		id1 = "11111111-1111-4111-8111-111111111111"
		id2 = "22222222-2222-4222-8222-222222222222"
	)

	// Counter family (2 keys).
	mustSet(t, mr, "usage:apikey:"+id1+":month_cost_usd", "12.50")
	mustSet(t, mr, "usage:apikey:"+id2+":month_cost_usd", "0.00")
	// Sticky-trip sentinel family (1 key).
	mustSet(t, mr, redisclient.CapTrippedSentinelPrefix+id1, "1")
	// Dedupe families — BOTH must be caught by the single dedupe glob (2 keys).
	mustSet(t, mr, redisclient.CapWarning80NotifiedPrefix+id1, "1")
	mustSet(t, mr, redisclient.CapTrippedNotifiedPrefix+id1, "1")

	// Unrelated keys that MUST survive (glob-isolation guard).
	survivors := []string{
		"auth:apikey:revoked:" + id1,              // Story-5.1 sentinel — different namespace
		"usage:apikey:" + id1 + ":request_count",  // foreign usage suffix
		"keystate:apikey:cap_warning_80_notified", // no trailing ":{id}" — must not match
		"unrelated:key",
	}
	for _, k := range survivors {
		mustSet(t, mr, k, "keep")
	}

	pc, err := redisclient.PurgeMonthlyState(ctx, rdb)
	if err != nil {
		t.Fatalf("PurgeMonthlyState: %v", err)
	}

	if pc.CounterKeys != 2 {
		t.Errorf("CounterKeys = %d, want 2", pc.CounterKeys)
	}
	if pc.SentinelKeys != 1 {
		t.Errorf("SentinelKeys = %d, want 1 (sticky-trip glob must NOT swallow cap_tripped_notified)", pc.SentinelKeys)
	}
	if pc.DedupeKeys != 2 {
		t.Errorf("DedupeKeys = %d, want 2 (dedupe glob must cover BOTH warning_80 + tripped families)", pc.DedupeKeys)
	}
	if got, want := pc.Total(), int64(5); got != want {
		t.Errorf("Total() = %d, want %d", got, want)
	}

	// Every cap-state key gone.
	for _, k := range []string{
		"usage:apikey:" + id1 + ":month_cost_usd",
		"usage:apikey:" + id2 + ":month_cost_usd",
		redisclient.CapTrippedSentinelPrefix + id1,
		redisclient.CapWarning80NotifiedPrefix + id1,
		redisclient.CapTrippedNotifiedPrefix + id1,
	} {
		if mr.Exists(k) {
			t.Errorf("key %q should have been deleted", k)
		}
	}
	// Every unrelated key survives.
	for _, k := range survivors {
		if !mr.Exists(k) {
			t.Errorf("unrelated key %q must NOT have been deleted (glob over-reach)", k)
		}
	}
}

// Scenario: 5.4-INT-026/031 (unit realisation — BR-3.15 empty-state happy path)
// Empty keyspace → all counts 0, no error.
func TestPurgeMonthlyState_EmptyKeyspaceIsHappyPath(t *testing.T) {
	t.Parallel()
	rdb, _ := newStandaloneRedis(t)

	pc, err := redisclient.PurgeMonthlyState(context.Background(), rdb)
	if err != nil {
		t.Fatalf("PurgeMonthlyState on empty keyspace: %v", err)
	}
	if pc.Total() != 0 {
		t.Fatalf("Total() = %d, want 0 on empty keyspace", pc.Total())
	}
}

// Scenario: 5.4-INT-020 (unit realisation — TC-9 idempotent re-run)
// Running the purge twice is benign: the second run finds nothing and reports
// zero counts without error.
func TestPurgeMonthlyState_IdempotentRerun(t *testing.T) {
	t.Parallel()
	rdb, mr := newStandaloneRedis(t)
	ctx := context.Background()

	const id = "33333333-3333-4333-8333-333333333333"
	mustSet(t, mr, "usage:apikey:"+id+":month_cost_usd", "1.00")
	mustSet(t, mr, redisclient.CapTrippedSentinelPrefix+id, "1")

	first, err := redisclient.PurgeMonthlyState(ctx, rdb)
	if err != nil {
		t.Fatalf("first purge: %v", err)
	}
	if first.Total() != 2 {
		t.Fatalf("first Total() = %d, want 2", first.Total())
	}

	second, err := redisclient.PurgeMonthlyState(ctx, rdb)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if second.Total() != 0 {
		t.Fatalf("second Total() = %d, want 0 (idempotent re-run)", second.Total())
	}
}

// Scenario: 5.4-INT-028 (unit realisation — Redis-down on SCAN error wrapping)
// A SCAN/DEL failure surfaces a wrapped error identifying the failing family
// ("purge counters: ...") rather than a bare driver error.
func TestPurgeMonthlyState_ScanFailureIsWrapped(t *testing.T) {
	t.Parallel()
	rdb, mr := newStandaloneRedis(t)

	mr.Close() // force every subsequent command to error (connection refused)

	_, err := redisclient.PurgeMonthlyState(context.Background(), rdb)
	if err == nil {
		t.Fatal("expected error when Redis is unavailable, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "purge counters") {
		t.Fatalf("error = %q, want it wrapped with %q", got, "purge counters")
	}
}

// Scenario: 5.4-UNIT (batching) — DEL flush boundary at capStateScanCount.
// Seeding more counter keys than one SCAN+DEL batch holds exercises both the
// mid-iteration flush and the final flush so the accumulated count is exact.
func TestPurgeMonthlyState_BatchedDeleteCountsExactly(t *testing.T) {
	t.Parallel()
	rdb, mr := newStandaloneRedis(t)
	ctx := context.Background()

	const n = 1003 // > capStateScanCount (1000) → at least one mid-iteration flush
	for i := 0; i < n; i++ {
		mustSet(t, mr, fmt.Sprintf("usage:apikey:%08d-0000-4000-8000-000000000000:month_cost_usd", i), "1")
	}

	pc, err := redisclient.PurgeMonthlyState(ctx, rdb)
	if err != nil {
		t.Fatalf("PurgeMonthlyState: %v", err)
	}
	if pc.CounterKeys != n {
		t.Fatalf("CounterKeys = %d, want %d (batched DEL must accumulate every flush)", pc.CounterKeys, n)
	}
}

func mustSet(t *testing.T, mr *miniredis.Miniredis, key, val string) {
	t.Helper()
	if err := mr.Set(key, val); err != nil {
		t.Fatalf("seed %q: %v", key, err)
	}
}
