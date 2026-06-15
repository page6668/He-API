// Tests for the plan-catalogue package (Story 7.8 AC3 substrate,
// Q-PLAN-CATALOG code-catalogue RATIFIED).
//
// Scenario trace -> docs/qa/assessments/7.8-test-design-20260610.md:
//
//	7.8-UNIT-001  catalogue defines exactly {free,pro,team,enterprise}, each -> Entitlement (BR-E-5)
//	7.8-UNIT-002  Entitlements(plan) returns rpm/tpm/qps + included-credit + quota + features per tier
//	7.8-UNIT-003  panic-at-construction when a plan key has no entitlement (1:1, 4.7 precedent)
//	7.8-UNIT-004  SandboxCeiling is a single catalogue entry (rpm/tpm/qps/quota) (Q-BETA-GATE)
//	7.8-UNIT-005  plan rank ordering free < pro < team < enterprise
//	7.8-UNIT-006  money fields (included_credit_usd, quota_usd) are string-decimal serialisable (Q-Spec-4)
//	7.8-UNIT-007  unknown plan lookup -> typed ErrUnknownPlan, NOT a panic
package plancatalogue

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// 7.8-UNIT-001 (P0) — the catalogue defines exactly the four ratified tiers,
// and every one resolves to a non-zero Entitlement (single SoT, BR-E-5).
func Test_UNIT_001_catalogue_defines_exactly_four_tiers(t *testing.T) {
	c := DefaultCatalogue
	want := []PlanKey{PlanFree, PlanPro, PlanTeam, PlanEnterprise}
	if c.Len() != len(want) {
		t.Fatalf("catalogue Len() = %d, want %d", c.Len(), len(want))
	}
	for _, k := range want {
		if !c.Has(k) {
			t.Errorf("catalogue missing tier %q", k)
		}
		ent, err := c.Entitlements(k)
		if err != nil {
			t.Errorf("Entitlements(%q) err = %v, want nil", k, err)
		}
		if ent.Plan != k {
			t.Errorf("Entitlements(%q).Plan = %q, want %q (self-describing)", k, ent.Plan, k)
		}
		if ent.RPM <= 0 || ent.TPM <= 0 || ent.QPS <= 0 {
			t.Errorf("Entitlements(%q) has non-positive axis: rpm=%d tpm=%d qps=%d", k, ent.RPM, ent.TPM, ent.QPS)
		}
	}
	// No extra tiers beyond the four.
	for _, p := range c.List() {
		found := false
		for _, k := range want {
			if p.Key == k {
				found = true
			}
		}
		if !found {
			t.Errorf("unexpected tier in catalogue: %q", p.Key)
		}
	}
}

// 7.8-UNIT-002 (P0) — each tier resolves the full Entitlement shape and the
// externally-anchored values are honoured (Pro $29, Free $5 credit, Enterprise
// rpm=10000). The exact non-anchored numbers are deploy-time-tunable; the test
// asserts shape + anchors + monotonicity, not every magic number.
func Test_UNIT_002_entitlements_resolve_full_shape_and_anchors(t *testing.T) {
	c := DefaultCatalogue

	free, _ := c.Find(PlanFree)
	if free.Entitlement.MonthlyIncludedCreditUSD != "5.00" {
		t.Errorf("free included credit = %q, want \"5.00\" (front-end-spec anchor)", free.Entitlement.MonthlyIncludedCreditUSD)
	}
	pro, _ := c.Find(PlanPro)
	if pro.PriceUSD != "29.00" {
		t.Errorf("pro price = %q, want \"29.00\" (front-end-spec anchor)", pro.PriceUSD)
	}
	ent, _ := c.Entitlements(PlanEnterprise)
	if ent.RPM != 10000 {
		t.Errorf("enterprise rpm = %d, want 10000 (AC2/QA anchor)", ent.RPM)
	}

	// Strict per-axis monotonic ladder free < pro < team < enterprise.
	order := []PlanKey{PlanFree, PlanPro, PlanTeam, PlanEnterprise}
	for i := 1; i < len(order); i++ {
		lo, _ := c.Entitlements(order[i-1])
		hi, _ := c.Entitlements(order[i])
		if !(hi.RPM > lo.RPM && hi.TPM > lo.TPM && hi.QPS > lo.QPS) {
			t.Errorf("axis ladder not strictly increasing %q->%q: %+v vs %+v", order[i-1], order[i], lo, hi)
		}
	}
}

// 7.8-UNIT-003 (P0) — a plan declared without an entitlement row panics at
// construction (forward 1:1).
func Test_UNIT_003_plan_without_entitlement_panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewFromRegistry did not panic on a plan missing its entitlement row")
		}
	}()
	NewFromRegistry(Registry{
		Plans:        []PlanSeed{{Key: PlanFree, Rank: 1}},
		Entitlements: map[PlanKey]Entitlement{}, // no row for free
	})
}

// reverse 1:1 — an entitlement row with no matching plan also panics.
func Test_UNIT_003b_entitlement_orphan_panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewFromRegistry did not panic on an orphan entitlement row")
		}
	}()
	NewFromRegistry(Registry{
		Plans: []PlanSeed{{Key: PlanFree, Rank: 1}},
		Entitlements: map[PlanKey]Entitlement{
			PlanFree: {RPM: 1, TPM: 1, QPS: 1},
			"orphan": {RPM: 1, TPM: 1, QPS: 1}, // no matching plan
		},
	})
}

// empty key + non-monotonic rank both panic (no silent half-load).
func Test_UNIT_003c_empty_key_and_nonmonotonic_rank_panic(t *testing.T) {
	t.Run("empty key", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic on empty plan key")
			}
		}()
		NewFromRegistry(Registry{
			Plans:        []PlanSeed{{Key: "", Rank: 1}},
			Entitlements: map[PlanKey]Entitlement{"": {RPM: 1, TPM: 1, QPS: 1}},
		})
	})
	t.Run("non-monotonic rank", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic on non-ascending rank")
			}
		}()
		NewFromRegistry(Registry{
			Plans: []PlanSeed{
				{Key: PlanFree, Rank: 2},
				{Key: PlanPro, Rank: 2}, // not strictly greater
			},
			Entitlements: map[PlanKey]Entitlement{
				PlanFree: {RPM: 1, TPM: 1, QPS: 1},
				PlanPro:  {RPM: 2, TPM: 2, QPS: 2},
			},
		})
	})
}

// 7.8-UNIT-004 (P1) — SandboxCeiling is a single catalogue entry carrying all
// four axes; rpm=60 is the AC2/QA anchor.
func Test_UNIT_004_sandbox_ceiling_single_entry(t *testing.T) {
	s := DefaultCatalogue.Sandbox()
	if s.RPM != 60 {
		t.Errorf("sandbox rpm = %d, want 60 (AC2/QA anchor)", s.RPM)
	}
	if s.TPM <= 0 || s.QPS <= 0 {
		t.Errorf("sandbox axes must be positive: %+v", s)
	}
	if s.MonthlyQuotaUSD == "" {
		t.Errorf("sandbox MonthlyQuotaUSD must be a string-decimal, got empty")
	}
}

// 7.8-UNIT-005 (P0) — plan rank ordering free < pro < team < enterprise; the
// direction resolver depends on it (BR-S-2).
func Test_UNIT_005_rank_ordering(t *testing.T) {
	c := DefaultCatalogue
	rFree, _ := c.Rank(PlanFree)
	rPro, _ := c.Rank(PlanPro)
	rTeam, _ := c.Rank(PlanTeam)
	rEnt, _ := c.Rank(PlanEnterprise)
	if !(rFree < rPro && rPro < rTeam && rTeam < rEnt) {
		t.Errorf("rank order broken: free=%d pro=%d team=%d enterprise=%d", rFree, rPro, rTeam, rEnt)
	}
	if _, ok := c.Rank("bogus"); ok {
		t.Errorf("Rank(bogus) ok=true, want false")
	}
}

// 7.8-UNIT-006 (P1) — money fields JSON-marshal as quoted strings, never bare
// numbers (Q-Spec-4 string-decimal discipline).
func Test_UNIT_006_money_fields_are_string_decimals(t *testing.T) {
	ent, _ := DefaultCatalogue.Entitlements(PlanPro)
	raw, err := json.Marshal(ent)
	if err != nil {
		t.Fatalf("marshal entitlement: %v", err)
	}
	js := string(raw)
	// The quota value must appear quoted (a string), e.g. "100.00", never 100.00.
	if !strings.Contains(js, `"`+ent.MonthlyQuotaUSD+`"`) {
		t.Errorf("MonthlyQuotaUSD not serialised as a quoted string: %s", js)
	}
	// A bare-number form (unquoted decimal) must NOT appear.
	if strings.Contains(js, ":"+ent.MonthlyQuotaUSD) {
		t.Errorf("MonthlyQuotaUSD leaked as a bare JSON number: %s", js)
	}

	pro, _ := DefaultCatalogue.Find(PlanPro)
	rawP, _ := json.Marshal(pro)
	if !strings.Contains(string(rawP), `"`+pro.PriceUSD+`"`) {
		t.Errorf("PriceUSD not serialised as a quoted string: %s", string(rawP))
	}
}

// 7.8-UNIT-007 (P1) — an unknown plan lookup returns the typed ErrUnknownPlan;
// it does NOT panic (panic is construction-time only).
func Test_UNIT_007_unknown_plan_lookup_typed_error(t *testing.T) {
	_, err := DefaultCatalogue.Entitlements("bogus")
	if !errors.Is(err, ErrUnknownPlan) {
		t.Errorf("Entitlements(bogus) err = %v, want ErrUnknownPlan", err)
	}
	if _, ok := DefaultCatalogue.Find("bogus"); ok {
		t.Errorf("Find(bogus) ok=true, want false")
	}
}

// 10.8-UNIT-001 (P0) — Story 10.8 AC1 revenue-promise invariant. The "$5 试用额度"
// of Story 10.8 (ratified OQ-10.8-1 = (b)) IS the EXISTING Free-tier monthly
// entitlement, not a new credit grant. Both money fields that jointly anchor the
// "$5" semantics must read "5.00": MonthlyIncludedCreditUSD (front-end-spec
// "Get Started — Free $5 credit") AND MonthlyQuotaUSD (Architect Round 1 Low —
// the Free quota is also $5, removing any ambiguity). A silent drift here would
// break the Beta-launch product promise without any compile error, so it is
// guarded as a P0 pure-config assertion.
func Test_UNIT_001_10_8_free_five_dollar_entitlement_invariant(t *testing.T) {
	free, ok := DefaultCatalogue.Find(PlanFree)
	if !ok {
		t.Fatal("Free tier missing from the default catalogue")
	}
	if free.Entitlement.MonthlyIncludedCreditUSD != "5.00" {
		t.Errorf("Free MonthlyIncludedCreditUSD = %q, want \"5.00\" (10.8 $5 trial anchor)",
			free.Entitlement.MonthlyIncludedCreditUSD)
	}
	if free.Entitlement.MonthlyQuotaUSD != "5.00" {
		t.Errorf("Free MonthlyQuotaUSD = %q, want \"5.00\" (10.8 $5 anchor, Architect Round 1 Low)",
			free.Entitlement.MonthlyQuotaUSD)
	}
}

// List() returns a defensive copy — mutating it must not affect the shared
// catalogue or other readers.
func Test_List_returns_defensive_copy(t *testing.T) {
	c := DefaultCatalogue
	first := c.List()
	if len(first) == 0 {
		t.Fatal("empty default catalogue")
	}
	first[0].DisplayName = "MUTATED"
	if c.List()[0].DisplayName == "MUTATED" {
		t.Errorf("List() leaked shared backing array")
	}
}

// Concurrent reads on the shared catalogue are race-free (run with -race).
func Test_concurrent_reads_race_free(t *testing.T) {
	c := DefaultCatalogue
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = c.List()
			_, _ = c.Find(PlanPro)
			_, _ = c.Entitlements(PlanEnterprise)
			_, _ = c.Rank(PlanFree)
			_ = c.Sandbox()
		}()
	}
	wg.Wait()
}
