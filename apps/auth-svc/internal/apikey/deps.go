// Story 5.1 — narrow dependency interfaces for the management surface.
//
// Defined HERE (not at call sites) so tests can substitute fakes without
// importing the production audit/redisclient packages. The package-public
// shapes mirror just the surface Service.Create/List/Revoke need —
// changes to the underlying packages don't ripple into apikey tests.

package apikey

import (
	"context"

	"github.com/google/uuid"

	"github.com/he-api/he-api/apps/auth-svc/internal/audit"
)

// AuditPublisher is the narrow Story-5.1 surface Service.Create / Revoke
// invoke after a successful PG commit. Wire production via the same
// audit.Publisher that auth-svc constructs in cmd/server/main.go (Kafka or
// NoOp depending on HE_API_AUDIT_KAFKA_BROKERS). On nil receiver
// Service.Create + Revoke skip the emit + log WARN; PG row remains the
// durable source of truth (BR-3.13 / Architect Q-Spec-3).
type AuditPublisher interface {
	Publish(ctx context.Context, event audit.Event) error
}

// SentinelStore is the narrow Story-5.1 BR-3.8 sentinel-write surface.
// Production wires *apps/auth-svc/internal/redisclient.RevokeSentinel which
// thinly wraps a *redis.Client. On nil receiver Service.Revoke skips the
// write + logs WARN per BR-3.13 fail-open default; the lag falls back to
// the security.md §8.2.1 5-minute cache-TTL baseline.
//
// TTL invariant: the SET MUST use a TTL ≥ the api-gateway positive-cache
// TTL (300 s) so a stale positive cannot outlive the sentinel that
// invalidates it. Architect Q2 ratified SentinelTTL = 300 s.
type SentinelStore interface {
	SetRevokedSentinel(ctx context.Context, apiKeyID uuid.UUID) error
}
