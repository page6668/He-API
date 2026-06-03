// Story 5.2 T1.3 — api_key config-updated sentinel writer.
//
// Mirrors revoke_sentinel.go verbatim (same package, same TTL contract) for
// the Story-5.2 BR-1.9 cross-pod cache-invalidation path. When a key's scope
// or monthly cost cap is mutated via UpdateApiKey, auth-svc SETs
// `auth:apikey:config_updated:{api_key_id}` EX 300; the api-gateway bearer
// hot path EXISTS-checks this key (alongside the revoke sentinel) on a
// positive cache hit and purges the stale extended-shape cache entry so the
// next request re-fetches the new policy via the Validate RPC.
package redisclient

import (
	"context"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ConfigUpdatedSentinelKeyPrefix is the Story-5.2 BR-1.9 cache-invalidation
// key prefix. Full key = ConfigUpdatedSentinelKeyPrefix + apiKeyID.String().
//
// MUST match the api-gateway bearer_auth.go constant of the same value — the
// two binaries communicate by reading/writing this key uncoordinated except
// for the shared string (a gateway-side unit cell asserts parity).
const ConfigUpdatedSentinelKeyPrefix = "auth:apikey:config_updated:"

// ConfigUpdatedSentinel wraps a *redis.Client to expose just the
// SetConfigUpdatedSentinel method that apikey.Service.UpdateApiKey requires.
// Reuses SentinelTTL (300s) from revoke_sentinel.go — the TTL invariant is
// identical (>= the api-gateway positive-cache TTL).
type ConfigUpdatedSentinel struct {
	Client *redis.Client
}

// NewConfigUpdatedSentinel returns a sentinel store backed by the supplied
// redis client. Caller MUST not pass nil; cmd/server validates the client is
// pingable before instantiating.
func NewConfigUpdatedSentinel(client *redis.Client) *ConfigUpdatedSentinel {
	return &ConfigUpdatedSentinel{Client: client}
}

// SetConfigUpdatedSentinel writes `ConfigUpdatedSentinelKeyPrefix+apiKeyID`
// with value "1" and TTL SentinelTTL. Returns any underlying Redis error;
// the caller (apikey.Service.UpdateApiKey) logs WARN + continues per the
// BR-1.9 fail-open default. The key's existence IS the signal (value opaque).
func (c *ConfigUpdatedSentinel) SetConfigUpdatedSentinel(ctx context.Context, apiKeyID uuid.UUID) error {
	key := ConfigUpdatedSentinelKeyPrefix + apiKeyID.String()
	return c.Client.Set(ctx, key, "1", SentinelTTL).Err()
}
