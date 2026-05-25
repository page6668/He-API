//go:build chaos
// +build chaos

// Story 5.1 — CHAOS-001..003 (Redis/PG/Kafka unavailability).
//
// Build tag `chaos` — NOT run in default CI; gated nightly suite per
// Story-4.x adapter precedent. Requires github.com/Shopify/toxiproxy +
// testcontainers (PG + Redis + Kafka).
//
// All three scenarios test fail-open / graceful-degradation invariants
// already covered at the unit level (5.1-UNIT-025 sentinel fail-open,
// 5.1-UNIT-009 audit fail, 5.1-UNIT-039 rate-limit fail-open). These
// chaos tests add wire-level verification using a real toxiproxy
// injection — verifying the same invariants under realistic Linux-pod
// network-failure semantics.

package chaos

import (
	"testing"
)

// TestKeysChaos covers CHAOS-001..003.
// Source: T9.4 (story line 627) + Dev Notes §Testing Requirements line 961-964.
func TestKeysChaos(t *testing.T) {
	t.Run("5.1-CHAOS-001 redis unavailable during revoke fails open", func(t *testing.T) {
		// Scenario: 5.1-CHAOS-001
		// REQUIRES: testcontainers + toxiproxy stack.
		// UNIT-LEVEL EQUIVALENT (already covered): 5.1-UNIT-025 verifies fail-open at the
		//   apikey.Service.Revoke layer when SetRevokedSentinel returns an error. CHAOS-001
		//   extends to a wire-level test: toxiproxy `down` proxy between auth-svc and
		//   Redis BEFORE the DELETE call, then verify 200 + slog WARN + degraded-mode
		//   cache-still-positive behaviour, then recover + advance time to verify 5-min
		//   §8.2.1 baseline fallback.
		t.Skip("requires toxiproxy + testcontainers (Redis); unit-level fail-open covered by 5.1-UNIT-025")
	})

	t.Run("5.1-CHAOS-002 PG unavailable during create returns 503 with no counter consume", func(t *testing.T) {
		// Scenario: 5.1-CHAOS-002
		// REQUIRES: testcontainers + toxiproxy stack.
		// UNIT-LEVEL EQUIVALENT (already covered): 5.1-UNIT-010 verifies CodeInternal
		//   surfaced on PG INSERT failure + zero Kafka emit. CHAOS-002 extends to the
		//   rate-limit refund pattern (BR-1.12): 11 failed POSTs → 11 × 503; then PG
		//   recovers; 10 successful POSTs follow + 1 hits 429 boundary.
		//
		// IMPLEMENTATION NOTE for the future fix: the gateway handler's CheckCreateKey-
		// RateLimit is invoked BEFORE the RPC; on RPC failure the counter is NOT refunded
		// in the current Story-5.1 impl. The BR-1.12 contract requires refund. The fix
		// would be either (a) refund on 502/503 via Redis DECR, or (b) move the rate-limit
		// check to AFTER successful RPC (consume-on-success semantics). SM marked this as
		// out-of-scope for the in-Story implementation; deferred to a future tracked
		// optimisation Story when the chaos suite goes live.
		t.Skip("requires toxiproxy + testcontainers (PG) + BR-1.12 refund-pattern impl (deferred); CodeInternal surfacing covered by 5.1-UNIT-010")
	})

	t.Run("5.1-CHAOS-003 kafka unavailable during create/revoke still returns success", func(t *testing.T) {
		// Scenario: 5.1-CHAOS-003
		// REQUIRES: testcontainers + toxiproxy stack.
		// UNIT-LEVEL EQUIVALENT (already covered): 5.1-UNIT-009 verifies audit publisher
		//   error path: CreateApiKey still returns 201 + slog WARN audit_emit_failed.
		//   CHAOS-003 extends to a wire-level test with toxiproxy down for Kafka.
		t.Skip("requires toxiproxy + testcontainers (Kafka); unit-level fail-open covered by 5.1-UNIT-009")
	})
}
