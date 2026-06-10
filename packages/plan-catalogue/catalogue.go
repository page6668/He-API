// Package plancatalogue is the single source of truth for the He-API
// subscription tier catalogue (Free / Pro / Team / Enterprise) and the
// per-tier Entitlement (rate-limit ceilings + monthly included-credit/quota +
// feature gates) that the gateway enforces on the chat hot path.
//
// Story 7.8 (Architect Round-1 Q-PLAN-CATALOG, RATIFIED): the catalogue is a
// version-controlled CODE catalogue — mirroring packages/models-catalogue —
// NOT a runtime-editable `plan_entitlements` PG table. Tier limits are a
// deploy-time product decision, so version-controlling them gives review +
// no per-pod drift + a trivial public `GET /v1/billing/plans` render. The 4.7
// 1:1 panic-at-construction invariant (every declared plan has exactly one
// entitlement row, and every entitlement row maps to a declared plan) moves
// here (Story 6.1 BR4-3 precedent) and is enforced at construction via panic —
// a drift here is a build-time programming error, not a runtime condition.
//
// The package carries PURE DOMAIN types only (no JSON tags, no HTTP/proto
// concerns — same posture as models-catalogue): the gateway maps Entitlement
// into ratelimit.Ceilings (BR-E-4 — supplies the LIMIT VALUES the 5.3 Lua
// limiter + 5.4 cap read; keys/scripts/envelopes untouched), billing-svc reads
// it for plan-change validation, and the api-gateway maps it into its public
// pricing response shape. Money fields are CANONICAL DECIMAL STRINGS (Q-Spec-4
// cascade): a quoted JSON string, never a JSON number.
package plancatalogue

import "errors"

// PlanKey is the catalogue key for a subscription tier. The four keys are the
// exact lowercase tokens accepted by PUT /v1/billing/subscription (BR-S-1) and
// stored opaquely in he_api.subscriptions.plan (Story 7.3, 0010).
type PlanKey string

// The four ratified tier keys (Q-TEAM-PLAN: `team` ships as a per-user tier;
// pooled/shared team quota is DEFERRED).
const (
	PlanFree       PlanKey = "free"
	PlanPro        PlanKey = "pro"
	PlanTeam       PlanKey = "team"
	PlanEnterprise PlanKey = "enterprise"
)

// ErrUnknownPlan is returned by Entitlements / Find / Rank for a key that is
// not in the catalogue. It is a LOOKUP-time typed error (UNIT-007) — distinct
// from the construction-time panic that guards the 1:1 declaration invariant
// (UNIT-003). A client-supplied unknown plan is an operational 400, never a
// crash.
var ErrUnknownPlan = errors.New("plancatalogue: unknown plan")

// Entitlement is the resolved per-tier budget the gateway enforces. The three
// rate-limit axes feed the Story-5.3 limiter (mapped to ratelimit.Ceilings by
// the gateway); MonthlyQuotaUSD feeds the Story-5.4 monthly cost-cap gate;
// MonthlyIncludedCreditUSD is the credit granted with the tier (the free tier's
// "$5 credit", front-end-spec landing). Money fields are canonical decimal
// strings (Q-Spec-4).
type Entitlement struct {
	Plan                     PlanKey
	RPM                      int      // requests per minute (5.3 axis)
	TPM                      int      // tokens per minute (5.3 axis)
	QPS                      int      // requests per second (5.3 axis)
	MonthlyIncludedCreditUSD string   // string-decimal, e.g. "5.00"
	MonthlyQuotaUSD          string   // string-decimal, feeds the 5.4 cap, e.g. "100.00"
	Features                 []string // feature gates (additive; empty = base)
}

// Plan is one fully-resolved catalogue row: its key, human display name, the
// monthly subscription price (string-decimal), a monotonic Rank used to resolve
// upgrade-vs-downgrade direction (BR-S-2), and the resolved Entitlement.
type Plan struct {
	Key         PlanKey
	DisplayName string
	PriceUSD    string // string-decimal monthly price, e.g. "29.00" (Pro, front-end-spec)
	Rank        int    // strict monotonic: free < pro < team < enterprise (UNIT-005)
	Entitlement Entitlement
}

// SandboxCeiling is the single global Beta-mode containment budget (Q-BETA-GATE).
// When flag:beta_mode is ON the gateway applies effective_limit =
// min(plan_limit, sandbox_limit) per axis with NO tier exemption (Enterprise is
// capped too). It lives in the catalogue as one entry so the sandbox budget is
// a reviewed, version-controlled value alongside the tiers.
type SandboxCeiling struct {
	RPM             int
	TPM             int
	QPS             int
	MonthlyQuotaUSD string // string-decimal
}

// Catalogue is an immutable, ordered view over the tier registry. Safe for
// concurrent reads: List returns a fresh copy and the lookup maps are read-only
// after construction.
type Catalogue struct {
	plans   []Plan           // registry-declaration order (load-bearing for List/pricing render)
	byKey   map[PlanKey]Plan // O(1) lookup
	sandbox SandboxCeiling
}

// NewFromRegistry materialises a Catalogue from a Registry, enforcing the 1:1
// plan↔entitlement invariant in BOTH directions and a strict-monotonic rank
// ordering. It PANICS — never returns a partially-loaded catalogue — because a
// drift here is a build-time programming error (4.7/6.1 precedent):
//
//   - a plan seed with an empty key;
//   - a plan with no entitlement row            (UNIT-003, 1:1 forward);
//   - an entitlement row with no matching plan  (reverse 1:1, no orphan);
//   - a duplicate plan key;
//   - a non-strict-monotonic Rank               (ambiguous upgrade/downgrade direction).
func NewFromRegistry(reg Registry) Catalogue {
	plans := make([]Plan, 0, len(reg.Plans))
	byKey := make(map[PlanKey]Plan, len(reg.Plans))

	lastRank := 0
	for i, seed := range reg.Plans {
		if seed.Key == "" {
			panic("plancatalogue: plan seed has empty key")
		}
		ent, ok := reg.Entitlements[seed.Key]
		if !ok {
			panic("plancatalogue: plan " + string(seed.Key) + " has no entitlement row")
		}
		if _, dup := byKey[seed.Key]; dup {
			panic("plancatalogue: duplicate plan key " + string(seed.Key))
		}
		if i > 0 && seed.Rank <= lastRank {
			panic("plancatalogue: plan " + string(seed.Key) + " rank not strictly greater than the previous plan (declaration order must be ascending rank)")
		}
		lastRank = seed.Rank

		// Bind the entitlement's Plan field to its key so a resolved
		// Entitlement is self-describing regardless of how it was looked up.
		ent.Plan = seed.Key
		p := Plan{
			Key:         seed.Key,
			DisplayName: seed.DisplayName,
			PriceUSD:    seed.PriceUSD,
			Rank:        seed.Rank,
			Entitlement: ent,
		}
		plans = append(plans, p)
		byKey[seed.Key] = p
	}

	// Reverse direction: every entitlement row must map to a declared plan
	// (no orphan).
	for key := range reg.Entitlements {
		if _, ok := byKey[key]; !ok {
			panic("plancatalogue: entitlement row " + string(key) + " has no matching plan")
		}
	}

	return Catalogue{plans: plans, byKey: byKey, sandbox: reg.Sandbox}
}

// List returns the catalogue plans in declaration order (free → enterprise).
// The result is a fresh copy on every call so callers (e.g. the public
// GET /v1/billing/plans render) may sort/filter freely.
func (c Catalogue) List() []Plan {
	out := make([]Plan, len(c.plans))
	copy(out, c.plans)
	return out
}

// Find returns the Plan for key and whether it exists.
func (c Catalogue) Find(key PlanKey) (Plan, bool) {
	p, ok := c.byKey[key]
	return p, ok
}

// Entitlements returns the resolved Entitlement for the given plan key, or
// ErrUnknownPlan for a key not in the catalogue (lookup-time error — never a
// panic; UNIT-007).
func (c Catalogue) Entitlements(key PlanKey) (Entitlement, error) {
	p, ok := c.byKey[key]
	if !ok {
		return Entitlement{}, ErrUnknownPlan
	}
	return p.Entitlement, nil
}

// Rank returns the monotonic rank for a plan key (free=lowest). The bool is
// false for an unknown key. Callers resolve upgrade (newRank > oldRank) vs
// downgrade (newRank < oldRank) from these (BR-S-2 / UNIT-005).
func (c Catalogue) Rank(key PlanKey) (int, bool) {
	p, ok := c.byKey[key]
	if !ok {
		return 0, false
	}
	return p.Rank, true
}

// Sandbox returns the global Beta-mode containment ceiling (Q-BETA-GATE).
func (c Catalogue) Sandbox() SandboxCeiling { return c.sandbox }

// Has reports whether key is a known catalogue plan. Convenience for the
// BR-S-1 validation path (unknown plan → 400_invalid_payment_request).
func (c Catalogue) Has(key PlanKey) bool {
	_, ok := c.byKey[key]
	return ok
}

// Len reports the number of tiers in the catalogue.
func (c Catalogue) Len() int { return len(c.plans) }
