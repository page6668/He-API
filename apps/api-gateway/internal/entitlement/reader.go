package entitlement

import (
	"context"
	"errors"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// SnapshotReader is the narrow Redis surface the resolver needs: a single GET on
// the hot path (BR-E-6 — cached-read only, NO PG/billing-svc round-trip). Both
// *redis.Client and a miniredis-backed client satisfy it.
type SnapshotReader interface {
	Get(ctx context.Context, key string) *redis.StringCmd
}

// BetaModeFunc reports whether global Beta-mode is currently ON for this pod
// (AC2). Injected so the entitlement resolver stays independent of the
// featureflag transport (Redis/PG/Unleash) and remains unit-testable. A nil
// BetaModeFunc is treated as OFF.
type BetaModeFunc func(ctx context.Context) bool

// Resolver computes the caller's effective ratelimit.Ceilings on the chat hot
// path from the cached entitlement snapshot + the in-process catalogue +
// (when ON) the global Beta sandbox ceiling. It is the production wiring behind
// the Story-5.3 ratelimit.ResolveCeilingsFunc seam (BR-E-4).
type Resolver struct {
	rdb      SnapshotReader
	cat      plancatalogue.Catalogue
	sandbox  plancatalogue.SandboxCeiling
	betaMode BetaModeFunc
	logger   *slog.Logger
}

// NewResolver builds a Resolver. betaMode may be nil (Beta treated as OFF —
// e.g. before the featureflag reader is wired). logger may be nil (defaults to
// slog.Default()).
func NewResolver(rdb SnapshotReader, cat plancatalogue.Catalogue, betaMode BetaModeFunc, logger *slog.Logger) *Resolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		rdb:      rdb,
		cat:      cat,
		sandbox:  cat.Sandbox(),
		betaMode: betaMode,
		logger:   logger,
	}
}

// CeilingsForUser resolves the effective per-axis ceilings for userID. Every
// uncertain path (no reader, empty user, Redis error, cache miss, corrupt
// snapshot, unknown plan) resolves to the FREE entitlement composed with the
// current Beta state (fail-safe-LOW, BR-E-2). The Beta sandbox, when ON, caps
// even a resolved higher tier (BR-B-1, no exemption).
func (r *Resolver) CeilingsForUser(ctx context.Context, userID string) ratelimit.Ceilings {
	raw, found := r.readSnapshot(ctx, userID)
	ent := Resolve(raw, found, r.cat)
	return Compose(ent, r.sandbox, r.betaOn(ctx))
}

// readSnapshot performs the single hot-path GET. redis.Nil (key absent) is a
// normal miss, not an error. Any other Redis error is logged once and treated
// as a miss → fail-safe-LOW (a Redis hiccup must throttle, never grant a higher
// tier). Returns (raw, found).
func (r *Resolver) readSnapshot(ctx context.Context, userID string) ([]byte, bool) {
	if r.rdb == nil || userID == "" {
		return nil, false
	}
	val, err := r.rdb.Get(ctx, SnapshotKey(userID)).Result()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			// Cohort/flag state is logged at debug only, PII-safe (BR — INT-037).
			r.logger.DebugContext(ctx, "entitlement_snapshot_read_failed",
				slog.String("error", err.Error()))
		}
		return nil, false
	}
	return []byte(val), true
}

func (r *Resolver) betaOn(ctx context.Context) bool {
	if r.betaMode == nil {
		return false
	}
	return r.betaMode(ctx)
}

// ResolveCeilings adapts the Resolver to the Story-5.3
// ratelimit.ResolveCeilingsFunc seam. It reads the authenticated owner user_id
// from the request context (Story-3.2 bearer-auth, BearerUserIDFromContext) —
// NOT the apiKeyID argument and NEVER a client-supplied value — and returns the
// composed ceilings. It never returns an error: every failure path is folded
// into fail-safe-LOW inside CeilingsForUser, so the limiter always receives a
// concrete, safe ceiling.
func (r *Resolver) ResolveCeilings() ratelimit.ResolveCeilingsFunc {
	return func(ctx context.Context, _ string) (ratelimit.Ceilings, error) {
		userID, _ := middleware.BearerUserIDFromContext(ctx)
		return r.CeilingsForUser(ctx, userID), nil
	}
}
