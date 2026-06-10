// Package featureflag is the gateway's read path for the global Beta-mode
// switch (Story 7.8 AC2, Q-BETA-GATE / Q-ADMIN-BETA RATIFIED).
//
// `flag:beta_mode` (Redis, data-models §4.3) is the RUNTIME read source — every
// gateway pod reads it on the hot path. The PG `he_api.feature_flags` row
// (0012) is the COLD-START SoT a booting pod reads before Unleash connects.
// Unleash (tech-stack §2.1) is the live push that keeps Redis fresh. PG is the
// authoritative store; Redis + Unleash are derived runtime mirrors (BR-B-2).
//
// There is NO He-API write surface for beta_mode (Q-ADMIN-BETA scope reduction):
// the flip is operated through the Unleash console (its own RBAC); this package
// is READ-ONLY and pod-local (BR-B-4 — INT-027/INT-029).
//
// The unavailable-direction is SPLIT into two distinct, separately-tested cases
// (Architect Q-BETA-GATE):
//
//   - BOOTING pod, no Redis AND no PG signal → Beta OFF. A cold infra fault must
//     NOT trap every user in the sandbox (UNIT-028).
//   - RUNNING pod that LOSES Redis → hold the LAST-KNOWN value until PG/Unleash
//     reconciles. A transient Redis blip must not flip the platform's
//     containment state (UNIT-029).
package featureflag

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"
)

// BetaModeRedisKey is the runtime Redis key (data-models §4.3).
const BetaModeRedisKey = "flag:beta_mode"

// BetaModeFlagKey is the PG he_api.feature_flags.key for the same flag (0012).
const BetaModeFlagKey = "beta_mode"

// RedisClient is the narrow runtime-read surface (a single GET on the hot path).
type RedisClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

// ColdStartReader reads the authoritative PG feature_flags row exactly when a
// booting pod has no runtime signal yet. It returns (enabled, found, err);
// found == false means the row is absent (treated as OFF). It is NOT called on
// the steady-state hot path — only as the booting-pod fallback.
type ColdStartReader func(ctx context.Context) (enabled bool, found bool, err error)

// Reader resolves the current Beta-mode state with the split fail-direction
// semantics. Build once per process and share; BetaOn is safe for concurrent
// use.
type Reader struct {
	rdb       RedisClient
	coldStart ColdStartReader
	logger    *slog.Logger

	mu        sync.RWMutex
	lastKnown *bool // nil ⇒ never resolved (booting); non-nil ⇒ last resolved value (running)
}

// NewReader builds a Reader. coldStart may be nil (no PG fallback — a booting
// pod with no Redis then resolves OFF). logger may be nil.
func NewReader(rdb RedisClient, coldStart ColdStartReader, logger *slog.Logger) *Reader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reader{rdb: rdb, coldStart: coldStart, logger: logger}
}

// BetaOn reports whether global Beta-mode is currently ON. It satisfies
// entitlement.BetaModeFunc. Resolution order:
//
//  1. Runtime read of `flag:beta_mode` from Redis. On a hit, cache it as
//     last-known and return it (UNIT-027 — Redis is the runtime source).
//  2. On a Redis miss/error with a LAST-KNOWN value present (running pod) →
//     return the last-known value (UNIT-029 — hold until reconcile).
//  3. On a Redis miss/error with NO last-known (booting pod) → consult the PG
//     cold-start reader; a PG hit becomes last-known; a PG miss/absence/error →
//     OFF (UNIT-028 — never trap users on a cold infra fault).
func (r *Reader) BetaOn(ctx context.Context) bool {
	if v, ok := r.runtimeRead(ctx); ok {
		r.setLastKnown(v)
		return v
	}

	// Redis gave no usable answer.
	if lk, ok := r.getLastKnown(); ok {
		return lk // running pod — hold last-known (UNIT-029)
	}

	// Booting pod, no runtime signal — fall back to the PG cold-start SoT.
	if r.coldStart != nil {
		enabled, found, err := r.coldStart(ctx)
		if err == nil && found {
			r.setLastKnown(enabled)
			return enabled
		}
		if err != nil {
			r.logger.WarnContext(ctx, "beta_mode_cold_start_read_failed",
				slog.String("error", err.Error()))
		}
	}
	// No Redis, no PG signal, booting — fail safe to OFF (UNIT-028).
	return false
}

// runtimeRead returns (value, ok). ok is false when Redis is nil, the key is
// absent (redis.Nil), or any Redis error occurs — all of which the caller
// resolves via the last-known / cold-start ladder.
func (r *Reader) runtimeRead(ctx context.Context) (bool, bool) {
	if r.rdb == nil {
		return false, false
	}
	val, err := r.rdb.Get(ctx, BetaModeRedisKey).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.logger.DebugContext(ctx, "beta_mode_runtime_read_failed",
				slog.String("error", err.Error()))
		}
		return false, false
	}
	return parseBool(val), true
}

func (r *Reader) setLastKnown(v bool) {
	r.mu.Lock()
	r.lastKnown = &v
	r.mu.Unlock()
}

func (r *Reader) getLastKnown() (bool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.lastKnown == nil {
		return false, false
	}
	return *r.lastKnown, true
}

// parseBool leniently parses the flag value. "1"/"true"/"on"/"enabled" (any
// case) ⇒ true; everything else ⇒ false. The toggle writer (Unleash sync)
// writes "1"/"0"; lenient parsing tolerates either convention.
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "on", "enabled", "t", "yes":
		return true
	default:
		return false
	}
}
