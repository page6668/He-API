package plancatalogue

// PlanSeed is one declared tier in a Registry: its key, display name, monthly
// price (string-decimal) and monotonic rank, in canonical ascending-rank order.
// Entitlements are held separately (in Registry.Entitlements) so the 1:1
// invariant between the two is a real, testable surface rather than a merged
// struct (4.7/6.1 precedent).
type PlanSeed struct {
	Key         PlanKey
	DisplayName string
	PriceUSD    string // string-decimal monthly price
	Rank        int    // strict-ascending in declaration order
}

// Registry is the raw catalogue seed: an ordered plan list + an entitlement map
// keyed by plan + the single SandboxCeiling. NewFromRegistry resolves these into
// a Catalogue, enforcing the 1:1 + monotonic-rank invariants.
type Registry struct {
	Plans        []PlanSeed
	Entitlements map[PlanKey]Entitlement
	Sandbox      SandboxCeiling
}

// DefaultRegistry is the authoritative 4-tier catalogue seed (Story 7.8,
// Q-PLAN-CATALOG code-catalogue RATIFIED).
//
// Externally-anchored values (from front-end-spec + the QA test design):
//   - Pro PriceUSD = "29.00"          (front-end-spec landing "Pro $29/mo")
//   - Free MonthlyIncludedCreditUSD = "5.00" (front-end-spec "Get Started — Free $5 credit")
//   - Enterprise RPM = 10000          (AC2 example / QA INT-025 anchor)
//   - SandboxCeiling RPM = 60         (AC2 example / QA INT-025 anchor)
//
// The remaining ceilings/prices are the initial product seed: a strict-monotonic
// ladder (free < pro < team < enterprise per axis). Per the Architect ruling
// these are a DEPLOY-TIME product decision (version-controlled, no per-pod
// drift) and may be re-tuned in a later release without a contract change — the
// binding invariants (4 tiers present, rank order, string-decimal money) are
// enforced by NewFromRegistry + the catalogue tests, not by these exact numbers.
var DefaultRegistry = Registry{
	Plans: []PlanSeed{
		{Key: PlanFree, DisplayName: "Free", PriceUSD: "0.00", Rank: 1},
		{Key: PlanPro, DisplayName: "Pro", PriceUSD: "29.00", Rank: 2},
		{Key: PlanTeam, DisplayName: "Team", PriceUSD: "99.00", Rank: 3},
		{Key: PlanEnterprise, DisplayName: "Enterprise", PriceUSD: "499.00", Rank: 4},
	},
	Entitlements: map[PlanKey]Entitlement{
		PlanFree: {
			RPM: 20, TPM: 40_000, QPS: 1,
			MonthlyIncludedCreditUSD: "5.00",
			MonthlyQuotaUSD:          "5.00",
			Features:                 nil,
		},
		PlanPro: {
			RPM: 200, TPM: 400_000, QPS: 5,
			MonthlyIncludedCreditUSD: "0.00",
			MonthlyQuotaUSD:          "100.00",
			Features:                 []string{"priority_support"},
		},
		PlanTeam: {
			RPM: 1_000, TPM: 2_000_000, QPS: 20,
			MonthlyIncludedCreditUSD: "0.00",
			MonthlyQuotaUSD:          "500.00",
			Features:                 []string{"priority_support", "shared_workspace"},
		},
		PlanEnterprise: {
			RPM: 10_000, TPM: 20_000_000, QPS: 50,
			MonthlyIncludedCreditUSD: "0.00",
			MonthlyQuotaUSD:          "5000.00",
			Features:                 []string{"priority_support", "shared_workspace", "sla", "dedicated_capacity"},
		},
	},
	// Global Beta containment budget (Q-BETA-GATE). RPM=60 is the AC2/QA anchor;
	// the other axes are a deliberately tight sandbox so even Enterprise is
	// meaningfully capped during Beta (min(plan, sandbox), no exemption).
	Sandbox: SandboxCeiling{
		RPM: 60, TPM: 120_000, QPS: 2,
		MonthlyQuotaUSD: "20.00",
	},
}

// DefaultCatalogue is the resolved DefaultRegistry. Built at package init, so
// any future drift (a plan without an entitlement, an orphan entitlement, a
// non-monotonic rank) panics at process start — preserving the fail-fast boot
// posture for the gateway, billing-svc and the public pricing endpoint.
var DefaultCatalogue = NewFromRegistry(DefaultRegistry)
