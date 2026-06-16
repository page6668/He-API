// Package userpref is the gateway hot-path resolver for the Story-6.5
// account-level default routing strategy (Q-A RULED: Option B — a gateway-side
// per-user cache read with sentinel invalidation, NOT a JWT `drs` claim, which
// would violate the CI-enforced BR-3.6 access-token claim-minimization invariant).
//
// On every chat request the gateway must know the caller's persisted default
// routing strategy WITHOUT a per-request PG/auth-svc round-trip on the common
// path. It does so by reading an auth-svc-OWNED cached value —
// `user:routing_pref:{user_id}` — with a single Redis GET, falling back to a
// lazy GetMe populate only on a cache miss. A config-updated sentinel —
// `user:pref_updated:{user_id}` (mirroring the Story-5.1/5.2
// `auth:apikey:config_updated:{id}` idiom) — forces a re-resolve after a save so
// the new default takes effect on the next request (seconds-bounded, NOT the
// 15-min token TTL of the overruled Option A).
//
// Direction of failure is non-negotiable: every uncertain path (no reader, empty
// user, Redis error, GetMe error) resolves to STRATEGY_UNSPECIFIED — the
// "no default" state, which the Story-6.2 cascade treats as STRATEGY_DEFAULT
// passthrough (fail-OPEN, Q-F). A convenience preference must NEVER block chat.
//
// This mirrors the Story-7.8 entitlement.Resolver seam (single hot-path GET,
// fail-safe on every uncertain path) and is wired behind the Decider's
// user-default callback in cmd/server/main.go.
package userpref

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/go-redis/v9"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

const (
	// RoutingPrefKeyPrefix is the auth-svc-OWNED per-user routing-preference key
	// stem. MUST match apps/auth-svc/internal/handlers/routing_pref_cache.go.
	RoutingPrefKeyPrefix = "user:routing_pref:"
	// PrefUpdatedSentinelPrefix is the config-updated sentinel stem (Story-5.1/5.2
	// idiom). MUST match the auth-svc writer.
	PrefUpdatedSentinelPrefix = "user:pref_updated:"

	// defaultTTL bounds a lazily-populated value; the next miss re-populates.
	defaultTTL = time.Hour
)

// RoutingPrefKey / PrefUpdatedKey build the per-user keys.
func RoutingPrefKey(userID string) string { return RoutingPrefKeyPrefix + userID }
func PrefUpdatedKey(userID string) string { return PrefUpdatedSentinelPrefix + userID }

// Cache is the narrow Redis surface the resolver needs: a single GET on the hot
// path, a conditional EXISTS on the sentinel, plus SetEx/Del for lazy-populate
// and invalidation consumption. Both *redis.Client and a miniredis-backed
// client satisfy it.
type Cache interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Exists(ctx context.Context, keys ...string) *redis.IntCmd
	SetEx(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// MeFetcher lazily populates the cache on a miss via the auth-svc GetMe RPC.
// The auth-svc Connect client satisfies it.
type MeFetcher interface {
	GetMe(ctx context.Context, req *connect.Request[authv1.GetMeRequest]) (*connect.Response[authv1.GetMeResponse], error)
}

// Resolver resolves userID → default routing strategy on the chat hot path.
// Construct once at startup; safe for concurrent use.
type Resolver struct {
	rdb    Cache
	me     MeFetcher
	ttl    time.Duration
	logger *slog.Logger
}

// NewResolver builds a Resolver. A nil rdb OR nil me disables resolution
// (ResolveUserDefault then always returns UNSPECIFIED → today's passthrough).
// logger may be nil (slog.Default()).
func NewResolver(rdb Cache, me MeFetcher, logger *slog.Logger) *Resolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{rdb: rdb, me: me, ttl: defaultTTL, logger: logger}
}

// ResolveUserDefault returns the caller's persisted default routing strategy, or
// STRATEGY_UNSPECIFIED on every uncertain path (fail-OPEN, Q-F). It matches the
// Decider's user-default callback signature.
func (r *Resolver) ResolveUserDefault(ctx context.Context, userID string) routingv1.Strategy {
	if r == nil || r.rdb == nil || userID == "" {
		return routingv1.Strategy_STRATEGY_UNSPECIFIED // BOUNDARY-001 / disabled
	}

	val, err := r.rdb.Get(ctx, RoutingPrefKey(userID)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// Cache MISS → lazy-populate via GetMe (UNIT-024).
			return r.lazyPopulate(ctx, userID)
		}
		// Any other Redis error → fail-OPEN none; chat never blocks (UNIT-026 /
		// BLIND-ERROR-002). Logged at debug, PII-safe.
		r.logger.DebugContext(ctx, "routing_pref_read_failed", slog.String("error", err.Error()))
		return routingv1.Strategy_STRATEGY_UNSPECIFIED
	}

	// Cache HIT. Check the invalidation sentinel (single EXISTS). When present, a
	// save has happened — re-resolve from auth-svc and DROP the stale value
	// (UNIT-027 / INT-007). A sentinel-check error is non-fatal: serve the hit.
	if n, sErr := r.rdb.Exists(ctx, PrefUpdatedKey(userID)).Result(); sErr == nil && n > 0 {
		_ = r.rdb.Del(ctx, PrefUpdatedKey(userID)).Err() // consume the invalidation
		return r.lazyPopulate(ctx, userID)
	}

	return parseStrategy(val)
}

// lazyPopulate fetches the authoritative value via GetMe, write-through caches
// it, and returns it. A GetMe error fails OPEN to UNSPECIFIED with a non-PII
// `user_default=unresolved` slog (UNIT-025 / BLIND-ERROR-001).
func (r *Resolver) lazyPopulate(ctx context.Context, userID string) routingv1.Strategy {
	if r.me == nil {
		return routingv1.Strategy_STRATEGY_UNSPECIFIED
	}
	resp, err := r.me.GetMe(ctx, connect.NewRequest(&authv1.GetMeRequest{UserId: userID}))
	if err != nil {
		r.logger.WarnContext(ctx, "routing_pref_unresolved",
			slog.String("event", "routing_pref_unresolved"),
			slog.String("user_default", "unresolved"), // non-PII; NEVER user_id
		)
		return routingv1.Strategy_STRATEGY_UNSPECIFIED
	}
	v := resp.Msg.GetDefaultRoutingStrategy() // "" when no default set
	// Write-through (best-effort) so subsequent requests hit the cache.
	_ = r.rdb.SetEx(ctx, RoutingPrefKey(userID), v, r.ttl).Err()
	return parseStrategy(v)
}

// parseStrategy maps the persisted enum string to the routingv1.Strategy. Any
// unrecognised/empty value → UNSPECIFIED (tier skipped → STRATEGY_DEFAULT).
func parseStrategy(s string) routingv1.Strategy {
	switch s {
	case "quality":
		return routingv1.Strategy_STRATEGY_QUALITY
	case "cost":
		return routingv1.Strategy_STRATEGY_COST
	case "latency":
		return routingv1.Strategy_STRATEGY_LATENCY
	default:
		return routingv1.Strategy_STRATEGY_UNSPECIFIED
	}
}
