package entitlement

import (
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// Resolve turns a cached snapshot (raw bytes + a found flag) into the resolved
// plan Entitlement. It fails safe-LOW to the FREE entitlement on EVERY
// uncertain path (BR-E-2):
//
//   - cache miss (found == false or empty bytes);
//   - a corrupt/malformed snapshot that will not decode;
//   - a snapshot naming a plan that is not in the catalogue.
//
// The ONLY inputs are the billing-svc-written snapshot and the in-process
// catalogue — there is no request-supplied field — so a client-asserted plan is
// structurally ignored (BR-E-1, UNIT-017). On every doubt the caller gets the
// lowest ceiling, never a higher one (UNIT-018/019/021).
func Resolve(raw []byte, found bool, cat plancatalogue.Catalogue) plancatalogue.Entitlement {
	free, _ := cat.Entitlements(plancatalogue.PlanFree)

	if !found || len(raw) == 0 {
		return free // cache miss → free (fail-safe-LOW)
	}
	snap, err := ParseSnapshot(raw)
	if err != nil {
		return free // corrupt snapshot → treated as miss → free
	}
	ent, err := cat.Entitlements(plancatalogue.PlanKey(snap.Plan))
	if err != nil {
		return free // unknown plan in snapshot → free (never fail-open-high)
	}
	return ent
}

// Compose maps a resolved plan Entitlement into ratelimit.Ceilings, applying
// the global Beta sandbox ceiling when betaOn is true.
//
// Composition is per-axis min(plan_limit, sandbox_limit) with NO tier exemption
// (Q-BETA-GATE / BR-B-1): even an Enterprise plan is capped at the sandbox
// ceiling during Beta — Beta is containment, there is no escape hatch
// (UNIT-024). When betaOn is false the plan ceiling stands alone (UNIT-025).
//
// Only the three rate-limit axes (rpm/tpm/qps) map to ratelimit.Ceilings; the
// monthly quota composes separately into the Story-5.4 cap (see QuotaUSD).
func Compose(ent plancatalogue.Entitlement, sandbox plancatalogue.SandboxCeiling, betaOn bool) ratelimit.Ceilings {
	c := ratelimit.Ceilings{
		QPSMax: ent.QPS,
		RPMMax: ent.RPM,
		TPMMax: ent.TPM,
	}
	if betaOn {
		c.QPSMax = minInt(c.QPSMax, sandbox.QPS)
		c.RPMMax = minInt(c.RPMMax, sandbox.RPM)
		c.TPMMax = minInt(c.TPMMax, sandbox.TPM)
	}
	return c
}

// minInt returns the smaller of a, b. (Local helper rather than the 1.21
// builtin to keep the composition explicit and axis-symmetric in tests.)
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
