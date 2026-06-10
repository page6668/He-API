// Package entitlement is the gateway hot-path tier-entitlement resolver
// (Story 7.8 AC3, Q-ENTITLEMENT-ENFORCE RATIFIED).
//
// On every chat request the gateway must know the caller's tier ceiling
// (rpm/tpm/qps) WITHOUT a per-request PG or billing-svc round-trip (BR-E-6
// latency budget). It does so by reading a CACHED snapshot —
// `entitlement:user:{id}` — that billing-svc is the SOLE writer of (BR-E-3),
// then resolving plan→limits from the in-process plan-catalogue (BR-E-5, single
// SoT, no per-pod drift). The resolution is keyed by the authenticated user_id
// (server-side); a client-asserted plan/limit is structurally ignored because
// the only inputs are the cached snapshot + the catalogue — never a request
// field (BR-E-1, privilege-escalation guard).
//
// Direction of failure is non-negotiable: a cache miss / corrupt snapshot /
// unknown plan resolves to the FREE ceiling (fail-safe-LOW, BR-E-2) — a cache
// hiccup throttles, it NEVER grants a higher tier.
//
// The resolved Entitlement is mapped into ratelimit.Ceilings and fed to the
// Story-5.3 Lua limiter via the stable ResolveCeilingsFunc seam; the limiter
// keys/scripts/envelopes are untouched (BR-E-4).
package entitlement

import "encoding/json"

// SnapshotKeyPrefix is the Redis key stem for the cached entitlement snapshot
// (docs/architecture/data-models.md §4.3, realised by Story 7.8). The full key
// is `entitlement:user:{user_id}`.
const SnapshotKeyPrefix = "entitlement:user:"

// SnapshotKey returns the cached-entitlement key for a user.
func SnapshotKey(userID string) string { return SnapshotKeyPrefix + userID }

// Snapshot is the cached `entitlement:user:{id}` value. billing-svc is the SOLE
// writer (BR-E-3); the gateway is a read-only consumer.
//
// It carries the resolved PLAN key — the authoritative binding — NOT the raw
// limit numbers. The gateway resolves plan→limits from its own in-process
// catalogue on read, so the enforced ceiling is always the gateway's
// deploy-time catalogue value (BR-E-5): a tampered or version-stale limit field
// can never grant a higher ceiling, and the plan→limit mapping cannot drift
// between the writer and the reader. `status` is informational (the writer only
// ever writes a snapshot for an ACTIVE plan; a non-active subscription is
// represented by snapshot ABSENCE → fail-safe-LOW free).
type Snapshot struct {
	Plan   string `json:"plan"`
	Status string `json:"status,omitempty"`
}

// Marshal encodes the snapshot for the cache. Used by tests and (via the same
// JSON shape) by the billing-svc writer.
func (s Snapshot) Marshal() ([]byte, error) { return json.Marshal(s) }

// ParseSnapshot decodes a cached snapshot. A decode error is treated by the
// resolver as a cache miss (fail-safe-LOW), so callers do not distinguish the
// error kind — they only need "did we get a usable plan key".
func ParseSnapshot(raw []byte) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return Snapshot{}, err
	}
	return s, nil
}
