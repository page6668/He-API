// Package subscription is billing-svc's tier plan-change orchestration (Story
// 7.8 AC1) and the SOLE writer of the gateway's cached entitlement snapshot
// (AC3, BR-E-3).
//
// It sits OVER the Story-7.3 subscription RAIL (he_api.subscriptions — the
// opaque `plan` string + status + provider + external_subscription_id) and adds
// the tier SEMANTICS: validate the requested plan against the catalogue, resolve
// upgrade-vs-downgrade by rank, drive the provider-subscription update through
// payment-svc, and apply the entitlement on the provider-confirmed webhook (the
// 7.3 "credit only on confirmed webhook" discipline).
//
// Money discipline (BR-S-4/5): proration is PROVIDER-computed (Stripe
// proration_behavior); this package issues NO balance credit/charge — it never
// touches he_api.balances (UNIT-015). The subscription charge reuses the 7.3
// provider rail + exactly-once webhook credit verbatim.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// ErrUnknownPlan is returned when the requested plan is not a catalogue tier
// (BR-S-1). The gateway maps it to the REUSED 7.3 envelope
// `400_invalid_payment_request` (Architect Medium-1 — NO new 400_invalid_plan).
var ErrUnknownPlan = errors.New("subscription: unknown plan")

// Direction is the rank comparison between the current and requested plan.
type Direction int

const (
	// DirectionSame — P_new == P_old (idempotent no-op; no provider call).
	DirectionSame Direction = iota
	// DirectionUpgrade — P_new ranks ABOVE P_old (immediate, BR-S-2).
	DirectionUpgrade
	// DirectionDowngrade — P_new ranks BELOW P_old (deferred to period end, BR-S-2).
	DirectionDowngrade
)

func (d Direction) String() string {
	switch d {
	case DirectionUpgrade:
		return "upgrade"
	case DirectionDowngrade:
		return "downgrade"
	default:
		return "same"
	}
}

// ResolveDirection compares old→new by catalogue rank. An absent/empty/unknown
// CURRENT plan resolves to free (Q-PLAN-DEFAULT) — a user with no active
// subscription upgrading is the common path. An unknown REQUESTED plan is a
// caller error → ErrUnknownPlan (BR-S-1).
func ResolveDirection(cat plancatalogue.Catalogue, oldPlan, newPlan plancatalogue.PlanKey) (Direction, error) {
	newRank, ok := cat.Rank(newPlan)
	if !ok {
		return DirectionSame, ErrUnknownPlan
	}
	oldRank, ok := cat.Rank(oldPlan)
	if !ok {
		// No / unknown current plan ⇒ treat as free (the lowest rank).
		oldRank, _ = cat.Rank(plancatalogue.PlanFree)
	}
	switch {
	case newRank > oldRank:
		return DirectionUpgrade, nil
	case newRank < oldRank:
		return DirectionDowngrade, nil
	default:
		return DirectionSame, nil
	}
}

// Subscription is the current durable subscription state (read from the 7.3
// he_api.subscriptions rail).
type Subscription struct {
	Plan                   plancatalogue.PlanKey
	Status                 string // active | cancelled | past_due
	Provider               string // stripe | paypal
	ExternalSubscriptionID string
	CurrentPeriodEnd       string // RFC3339 (next-billing / period roll); "" when unknown
}

// SubReader reads a user's current subscription. found == false means no active
// subscription row → the user is on free (Q-PLAN-DEFAULT). Abstracts the SELECT
// so ChangePlan logic is unit-testable without a live PG.
type SubReader interface {
	CurrentSubscription(ctx context.Context, userID string) (sub Subscription, found bool, err error)
}

// ProviderUpdater drives the provider-subscription change via payment-svc
// (UpdateProviderSubscription). `prorate` requests provider-side proration
// (Stripe proration_behavior) for an immediate upgrade; a deferred downgrade
// passes prorate=false and atPeriodEnd=true. The provider is the proration money
// authority — this call issues NO He-API credit (BR-S-4).
type ProviderUpdater interface {
	UpdateProviderSubscription(ctx context.Context, provider, extSubID, newPlan string, prorate, atPeriodEnd bool) error
}

// EntitlementInvalidator is the cache side-effect surface (the snapshot writer).
// On an immediate upgrade the snapshot is written optimistically so the new
// entitlement applies within the stale window (Q-UPGRADE-DOWNGRADE knob,
// UNIT-014); a downgrade/cancel does NOT change the snapshot here (the higher
// entitlement is kept until the period rolls — the webhook/period-roll invalidates).
type EntitlementInvalidator interface {
	WriteActive(ctx context.Context, userID string, plan plancatalogue.PlanKey) error
}

// ChangeResult reports what ChangePlan did (for the handler response + tests).
type ChangeResult struct {
	Direction           Direction
	NewPlan             plancatalogue.PlanKey
	ProviderCalled      bool // a provider update was issued
	DeferredToPeriodEnd bool // downgrade/cancel scheduled at period end
}

// Service orchestrates plan changes. Construct once and share.
type Service struct {
	cat      plancatalogue.Catalogue
	subs     SubReader
	provider ProviderUpdater
	snapshot EntitlementInvalidator
	logger   *slog.Logger
}

// NewService builds the orchestrator. snapshot may be nil (the optimistic
// upgrade snapshot is then skipped — the webhook still converges it).
func NewService(cat plancatalogue.Catalogue, subs SubReader, provider ProviderUpdater, snapshot EntitlementInvalidator, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{cat: cat, subs: subs, provider: provider, snapshot: snapshot, logger: logger}
}

// ChangePlan executes a tier change for the AUTHENTICATED user (userID is always
// server-resolved by the caller; there is no target-user parameter — BR-S-7
// cross-user mutation is impossible by construction).
//
// Flow (BR-S-1/2/3):
//  1. Validate newPlan against the catalogue (unknown → ErrUnknownPlan, NO
//     provider call, NO mutation).
//  2. Read the current subscription (absent → free).
//  3. Same plan → idempotent no-op (NO provider call) — UNIT-011.
//  4. Upgrade → provider update with proration, IMMEDIATE; optimistically write
//     the entitlement snapshot (UNIT-009/014). subscriptions.plan is NOT mutated
//     here — it flips on the confirmed 7.3 webhook (BR-S-3, UNIT-013).
//  5. Downgrade → provider update scheduled at current_period_end, DEFERRED; the
//     entitlement is unchanged now (kept until the period rolls) — UNIT-010.
//
// It NEVER touches he_api.balances (UNIT-015).
func (s *Service) ChangePlan(ctx context.Context, userID string, newPlan plancatalogue.PlanKey) (ChangeResult, error) {
	if !s.cat.Has(newPlan) {
		return ChangeResult{}, ErrUnknownPlan // BR-S-1 — no provider call, no mutation
	}

	cur := plancatalogue.PlanFree
	var extID, provider string
	if sub, found, err := s.subs.CurrentSubscription(ctx, userID); err != nil {
		return ChangeResult{}, fmt.Errorf("change-plan: read current subscription: %w", err)
	} else if found {
		cur, extID, provider = sub.Plan, sub.ExternalSubscriptionID, sub.Provider
	}

	dir, err := ResolveDirection(s.cat, cur, newPlan)
	if err != nil {
		return ChangeResult{}, err
	}

	res := ChangeResult{Direction: dir, NewPlan: newPlan}
	switch dir {
	case DirectionSame:
		// Idempotent no-op — return current, NO provider call (UNIT-011).
		return res, nil

	case DirectionUpgrade:
		// Immediate: provider update WITH proration. The webhook confirms the
		// durable plan flip; we optimistically warm the entitlement snapshot so
		// the new ceiling applies within the stale window.
		if err := s.provider.UpdateProviderSubscription(ctx, provider, extID, string(newPlan), true /*prorate*/, false /*atPeriodEnd*/); err != nil {
			return ChangeResult{}, err // provider-domain failure → caller maps to 402 (no transition)
		}
		res.ProviderCalled = true
		if s.snapshot != nil {
			if err := s.snapshot.WriteActive(ctx, userID, newPlan); err != nil {
				// Non-fatal: the webhook + TTL still converge. Log and proceed.
				s.logger.WarnContext(ctx, "entitlement_snapshot_optimistic_write_failed",
					slog.String("direction", dir.String()), slog.String("error", err.Error()))
			}
		}
		return res, nil

	default: // DirectionDowngrade
		// Deferred to current_period_end: provider update scheduled at period end,
		// NO proration credit. Entitlement unchanged now (kept until the roll).
		if err := s.provider.UpdateProviderSubscription(ctx, provider, extID, string(newPlan), false /*prorate*/, true /*atPeriodEnd*/); err != nil {
			return ChangeResult{}, err
		}
		res.ProviderCalled = true
		res.DeferredToPeriodEnd = true
		return res, nil
	}
}
