// Package redisclient — Story 5.1 T3.2.
//
// Hosts the api_key revoke-sentinel writer. Defined as a discrete package
// so:
//   - apikey.Service depends on the narrow SentinelStore interface (no
//     transitive go-redis import in the apikey unit tests).
//   - The api-gateway side (bearer_auth.go) reads the same `auth:apikey:
//     revoked:{api_key_id}` key by string-prefix convention — defined as
//     `SentinelKeyPrefix` here AND mirrored gateway-side, with a 5.1-UNIT
//     cell asserting parity (covered by gateway's bearer_auth_test.go).
package redisclient

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// SentinelKeyPrefix is the Story-5.1 BR-3.8 cross-pod cache-invalidation
// key prefix. Full key = SentinelKeyPrefix + apiKeyID.String().
//
// MUST match the api-gateway bearer_auth.go constant of the same name +
// value — the two binaries communicate by reading/writing this key
// uncoordinated except for the shared string. A 5.1-UNIT cell asserts
// parity via a Go-test reflection check at the boundary.
const SentinelKeyPrefix = "auth:apikey:revoked:"

// SentinelTTL is the Story-5.1 BR-3.8 + Architect Q2 ratified sentinel
// TTL. MUST be >= the api-gateway positive-cache TTL (300s) so a stale
// positive cache entry cannot outlive the sentinel that invalidates it.
const SentinelTTL = 300 * time.Second

// RevokeSentinel wraps a *redis.Client to expose just the
// SetRevokedSentinel method that apikey.Service requires. Defined as a
// concrete struct (not a function var) so production wiring uses a single
// constructor at cmd/server boot and tests can substitute via the
// apikey.SentinelStore interface.
type RevokeSentinel struct {
	Client *redis.Client
}

// NewRevokeSentinel returns a sentinel store backed by the supplied redis
// client. Caller MUST not pass nil; cmd/server validates the client is
// pingable before instantiating.
func NewRevokeSentinel(client *redis.Client) *RevokeSentinel {
	return &RevokeSentinel{Client: client}
}

// SetRevokedSentinel writes `SentinelKeyPrefix+apiKeyID` with value "1"
// and TTL SentinelTTL. Returns any underlying Redis error; the caller
// (apikey.Service.Revoke) logs WARN + continues per BR-3.13 fail-open.
//
// The value is a literal "1" so a gateway-side EXISTS check returns 1
// (presence indicator); the value carries no semantic content (the key's
// existence IS the signal). Future stories may extend the value to carry
// revocation-reason metadata; the current contract treats it as opaque.
func (r *RevokeSentinel) SetRevokedSentinel(ctx context.Context, apiKeyID uuid.UUID) error {
	key := SentinelKeyPrefix + apiKeyID.String()
	return r.Client.Set(ctx, key, "1", SentinelTTL).Err()
}
