// Story 5.4 AC3 — monthly cost-reset Redis purge.
//
// PurgeMonthlyState SCAN+DELs the three cost-cap key families at the UTC month
// boundary. The prefixes mirror the gateway writer
// (apps/api-gateway/internal/middleware/keypolicy/cap_state.go) and the
// notification-svc dedupe writer
// (apps/notification-svc/internal/handlers/cap_threshold.go) — the cron's SCAN
// MUST cover exactly the keys those writers create or the breaker reset is
// partial.
//
// Cluster-mode aware (Architect Round 1 m-1): on a *redis.ClusterClient the
// keyspace is sharded across masters, and a plain Scan only enumerates the
// shard the client first connects to. PurgeMonthlyState dispatches via
// ForEachMaster so every shard is visited; standalone clients use a single
// Scan iterator.
package redisclient

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/redis/go-redis/v9"
)

// Cap-state Redis key prefixes (no TTL — cron-cleared). MUST match the gateway
// + notification-svc constants of the same name/value.
const (
	CapTrippedSentinelPrefix   = "keystate:apikey:cap_tripped:"
	CapWarning80NotifiedPrefix = "keystate:apikey:cap_warning_80_notified:"
	CapTrippedNotifiedPrefix   = "keystate:apikey:cap_tripped_notified:"
)

// SCAN MATCH patterns (BR-3.5 family glob). The dedupe glob covers BOTH the
// warning + tripped notified families in a single pass.
const (
	usageCounterMatch    = "usage:apikey:*:month_cost_usd"
	capTrippedMatch      = CapTrippedSentinelPrefix + "*"
	capNotifiedDedupMatch = "keystate:apikey:cap_*_notified:*"
)

// capStateScanCount is the SCAN COUNT hint (Q-D ratified).
const capStateScanCount = 1000

// PurgeCounts reports per-family deletion totals for the cron's structured log.
type PurgeCounts struct {
	CounterKeys  int64 // usage:apikey:*:month_cost_usd
	SentinelKeys int64 // keystate:apikey:cap_tripped:*
	DedupeKeys   int64 // keystate:apikey:cap_*_notified:*
}

// Total is the sum across families.
func (p PurgeCounts) Total() int64 { return p.CounterKeys + p.SentinelKeys + p.DedupeKeys }

// scanDeleter is the narrow Scan+Del surface satisfied by *redis.Client (both
// a standalone client and the per-master client ForEachMaster hands us).
type scanDeleter interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// PurgeMonthlyState deletes all three cost-cap key families. Accepts any
// redis.UniversalClient; cluster clients fan out over every master.
func PurgeMonthlyState(ctx context.Context, rdb redis.UniversalClient) (PurgeCounts, error) {
	var pc PurgeCounts
	var err error
	if pc.CounterKeys, err = purgePattern(ctx, rdb, usageCounterMatch); err != nil {
		return pc, fmt.Errorf("purge counters: %w", err)
	}
	if pc.SentinelKeys, err = purgePattern(ctx, rdb, capTrippedMatch); err != nil {
		return pc, fmt.Errorf("purge sentinels: %w", err)
	}
	if pc.DedupeKeys, err = purgePattern(ctx, rdb, capNotifiedDedupMatch); err != nil {
		return pc, fmt.Errorf("purge dedupe: %w", err)
	}
	return pc, nil
}

// purgePattern SCAN+DELs one MATCH pattern, fanning out over cluster masters.
func purgePattern(ctx context.Context, rdb redis.UniversalClient, match string) (int64, error) {
	switch c := rdb.(type) {
	case *redis.ClusterClient:
		var total int64
		// ForEachMaster runs fn concurrently per master → atomic accumulate.
		err := c.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
			n, e := scanDelNode(ctx, master, match)
			atomic.AddInt64(&total, n)
			return e
		})
		return atomic.LoadInt64(&total), err
	case scanDeleter:
		return scanDelNode(ctx, c, match)
	default:
		return 0, fmt.Errorf("unsupported redis client type %T", rdb)
	}
}

// scanDelNode iterates one node's keyspace for `match`, DELeting in batches of
// capStateScanCount. go-redis transparently follows MOVED redirections during a
// single master's iteration (BR-3.7).
func scanDelNode(ctx context.Context, node scanDeleter, match string) (int64, error) {
	var deleted int64
	batch := make([]string, 0, capStateScanCount)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := node.Del(ctx, batch...).Result()
		if err != nil {
			return err
		}
		deleted += n
		batch = batch[:0]
		return nil
	}

	iter := node.Scan(ctx, 0, match, capStateScanCount).Iterator()
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) >= capStateScanCount {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if err := iter.Err(); err != nil {
		return deleted, err
	}
	if err := flush(); err != nil {
		return deleted, err
	}
	return deleted, nil
}
