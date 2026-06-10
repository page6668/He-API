package subscription

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// SnapshotTTL is the self-heal ceiling for the cached entitlement snapshot
// (Q-ENTITLEMENT-ENFORCE, Architect knob: TTL ≤ 60s). A stale snapshot that the
// writer somehow failed to invalidate expires within this window, after which
// the gateway resolves the user to free (fail-safe-LOW) until a refresh.
const SnapshotTTL = 60 * time.Second

// snapshotKeyPrefix MUST match the gateway reader
// (apps/api-gateway/internal/entitlement.SnapshotKeyPrefix). Cross-service
// contract: billing-svc writes `entitlement:user:{id}` as {"plan":...,"status":...}.
const snapshotKeyPrefix = "entitlement:user:"

func snapshotKey(userID string) string { return snapshotKeyPrefix + userID }

// sentinelKey is the cross-pod invalidation marker (5.1 `auth:apikey:revoked`
// precedent). Set with a short TTL on a downgrade/cancel so any pod that still
// holds a positively-cached snapshot treats the user as invalidated → free.
func sentinelKey(userID string) string { return snapshotKeyPrefix + userID + ":invalidated" }

// snapshotValue is the JSON shape shared with the gateway reader
// (entitlement.Snapshot). It carries the authoritative PLAN key; the gateway
// resolves plan→limits from its own catalogue (BR-E-5, no per-pod drift).
type snapshotValue struct {
	Plan   string `json:"plan"`
	Status string `json:"status,omitempty"`
}

// Redis is the minimal go-redis surface the snapshot writer needs.
type Redis interface {
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// SnapshotWriter is billing-svc's SOLE-writer surface for the gateway entitlement
// cache (BR-E-3). The gateway is read-only; no other service writes these keys.
type SnapshotWriter struct {
	rdb    Redis
	logger *slog.Logger
}

// NewSnapshotWriter builds the writer. rdb may be nil (writes become no-ops with
// a one-shot warn — the webhook + the gateway's fail-safe-LOW still hold).
func NewSnapshotWriter(rdb Redis, logger *slog.Logger) *SnapshotWriter {
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotWriter{rdb: rdb, logger: logger}
}

// WriteActive writes the entitlement snapshot for an ACTIVE plan with a bounded
// TTL (≤ 60s) and clears any stale invalidation sentinel. Called on an upgrade
// (optimistic) and on the confirmed activate/renew webhook. Satisfies
// subscription.EntitlementInvalidator.
func (w *SnapshotWriter) WriteActive(ctx context.Context, userID string, plan plancatalogue.PlanKey) error {
	if w.rdb == nil || userID == "" {
		return nil
	}
	val, err := json.Marshal(snapshotValue{Plan: string(plan), Status: "active"})
	if err != nil {
		return err
	}
	if err := w.rdb.Set(ctx, snapshotKey(userID), val, SnapshotTTL).Err(); err != nil {
		return err
	}
	// Clear the invalidation sentinel — this user is now affirmatively active.
	_ = w.rdb.Del(ctx, sentinelKey(userID)).Err()
	w.logger.InfoContext(ctx, "entitlement_snapshot_written",
		slog.String("event", "entitlement_snapshot_written"),
		slog.String("user_id", userID),
		slog.String("plan", string(plan)))
	return nil
}

// Invalidate removes the snapshot and sets a short-TTL invalidation sentinel, so
// the gateway converges the user to the free ceiling within the stale window
// (downgrade-at-period-end roll, cancel, or a past_due/cancelled webhook). The
// gateway read then misses → fail-safe-LOW free (it NEVER keeps the higher tier).
func (w *SnapshotWriter) Invalidate(ctx context.Context, userID string) error {
	if w.rdb == nil || userID == "" {
		return nil
	}
	if err := w.rdb.Del(ctx, snapshotKey(userID)).Err(); err != nil {
		return err
	}
	if err := w.rdb.Set(ctx, sentinelKey(userID), "1", SnapshotTTL).Err(); err != nil {
		return err
	}
	w.logger.InfoContext(ctx, "entitlement_snapshot_invalidated",
		slog.String("event", "entitlement_snapshot_invalidated"),
		slog.String("user_id", userID))
	return nil
}
