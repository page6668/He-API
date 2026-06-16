// Story 6.5 — account-level default routing strategy: validation + the
// auth-svc-owned cache write-through that backs the gateway hot-path resolver
// (Architect Q-A ruling: Option B — gateway per-user cache read + sentinel
// invalidation, NOT a JWT claim).
//
// auth-svc OWNS these keys (it owns the profile write). The gateway is a
// read-only consumer that lazy-populates on miss via the GetMe RPC. The key
// format below MUST stay in lock-step with the gateway `userpref` package
// (apps/api-gateway/internal/userpref) — duplicated with this comment rather
// than shared because auth-svc and api-gateway are separate workspace modules
// (same convention as validLocales ↔ console i18n/config.ts).
package handlers

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// routingPrefKeyPrefix is the auth-svc-owned per-user routing-preference key
	// stem. Full key: `user:routing_pref:{user_id}`. Value = the strategy string
	// ("quality"|"cost"|"latency") or "" for "no default". DELIBERATELY NOT
	// `entitlement:user:{id}` (billing-svc is its sole writer — BR-E-3).
	routingPrefKeyPrefix = "user:routing_pref:"
	// routingPrefUpdatedSentinelPrefix is the config-updated sentinel stem,
	// mirroring the Story-5.1/5.2 `auth:apikey:config_updated:{id}` idiom. Full
	// key: `user:pref_updated:{user_id}`. The gateway checks it (single EXISTS)
	// on a cache hit and re-resolves when present (INT-005/INT-007, UNIT-027).
	routingPrefUpdatedSentinelPrefix = "user:pref_updated:"

	// routingPrefTTL bounds the cached value; the gateway re-populates on miss.
	routingPrefTTL = time.Hour
	// routingPrefSentinelTTL bounds the invalidation signal — long enough for
	// every gateway pod to observe it, short enough to self-clean.
	routingPrefSentinelTTL = 5 * time.Minute
)

// validDefaultRoutingStrategies is the enum surface for the account default.
// STRATEGY_DEFAULT/UNSPECIFIED/he-router-* are per-request directives, NOT
// account preferences (Q-D), so they are NOT user-selectable here.
var validDefaultRoutingStrategies = map[string]bool{
	"quality": true,
	"cost":    true,
	"latency": true,
}

// routingPrefKey / routingPrefUpdatedKey build the per-user keys.
func routingPrefKey(userID string) string        { return routingPrefKeyPrefix + userID }
func routingPrefUpdatedKey(userID string) string { return routingPrefUpdatedSentinelPrefix + userID }

// writeThroughRoutingPref is the best-effort write-through invoked AFTER a
// successful UpdateProfile that touched default_routing_strategy (INT-005). It
// sets the cached value (or "" when cleared) and the invalidation sentinel.
// Failure is non-fatal — it NEVER blocks the 200 (the gateway lazy-populates on
// the next cache miss anyway); the caller logs at WARN.
func writeThroughRoutingPref(ctx context.Context, rdb redis.Cmdable, userID string, value *string) error {
	if rdb == nil || userID == "" {
		return nil
	}
	v := ""
	if value != nil {
		v = *value
	}
	if err := rdb.SetEx(ctx, routingPrefKey(userID), v, routingPrefTTL).Err(); err != nil {
		return err
	}
	// Sentinel forces every gateway pod to re-resolve on its next hit, closing
	// the cross-pod stale-routing window (BLIND-CONCURRENCY-002).
	return rdb.Set(ctx, routingPrefUpdatedKey(userID), "1", routingPrefSentinelTTL).Err()
}
