// Tests for Story 10.8 AC1 — "Beta 模式下新验证用户经 default-to-free 享 Free $5
// 月度权益 + Beta Sandbox gate". Ratified scope = (b): NO backend money code.
// These tests VERIFY that the existing 7.8 read path (entitlement.Resolve +
// entitlement.Compose, gated by featureflag.Reader.BetaOn) already delivers the
// AC1 behavior — and GUARD that no money-write creeps into that path.
//
// Scenario trace -> docs/qa/assessments/10.8-test-design-20260616.md:
//
//	10.8-UNIT-002  default-to-free resolves Free entitlement for a user with NO
//	               subscription row — no grant action (BR-10.8.2)
//	10.8-INT-001   new-verified user (no subscription) + beta_mode=ON -> resolves
//	               Free $5 AND is Beta-gated into Sandbox (composition)
//	10.8-INT-002   beta_mode=OFF (default) -> Free user on the GA path, not Sandbox
//	10.8-INT-004   zero-money-write: the Beta read path is pure — no balances write
//	               / no credit accumulation (§9.2 deduction-skip)
//
// UNIT-003/-004 (Beta gate fail-safe-OFF / running-loses-Redis) are the existing
// 7.8 UNIT-028/029 in featureflag/betamode_test.go (REUSE, asserted green) and
// are not re-authored here (duplicate-coverage guard).
package entitlement

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/featureflag"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// 10.8-UNIT-002 (P0) — a newly-verified user with NO subscription row resolves to
// the Free entitlement automatically via default-to-free (cache miss -> Free),
// carrying the $5 monthly included credit. There is NO grant action: Resolve is a
// pure read of (snapshot, catalogue) and there is no money-write API on this path.
func Test_UNIT_002_10_8_default_to_free_no_grant(t *testing.T) {
	free, _ := cat().Entitlements(plancatalogue.PlanFree)

	// No subscription row == cache miss (found=false) -> default-to-free.
	got := Resolve(nil, false /* found */, cat())

	if got.Plan != plancatalogue.PlanFree {
		t.Errorf("default-to-free plan = %q, want free (no subscription row)", got.Plan)
	}
	if got.MonthlyIncludedCreditUSD != "5.00" {
		t.Errorf("default-to-free included credit = %q, want \"5.00\" (existing Free $5, no grant)",
			got.MonthlyIncludedCreditUSD)
	}
	if got.RPM != free.RPM || got.TPM != free.TPM || got.QPS != free.QPS {
		t.Errorf("default-to-free = %+v, want free %+v", got, free)
	}
}

// betaReader wires a real featureflag.Reader over miniredis so the gate value is
// resolved exactly as the gateway resolves it (BR-10.8.3 — reuse, don't redefine).
func betaReader(t *testing.T, on bool) *featureflag.Reader {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	if on {
		mr.Set(featureflag.BetaModeRedisKey, "1")
	} else {
		mr.Set(featureflag.BetaModeRedisKey, "0")
	}
	return featureflag.NewReader(rdb, nil, nil)
}

// 10.8-INT-001 (P0) — composition: a new-verified user with no subscription +
// beta_mode=ON resolves the Free $5 entitlement AND is Beta-gated into the
// Sandbox (the ON branch of Compose contains every axis to <= the sandbox
// ceiling, no tier exemption — BR-10.8.3 / 7.8 Q-BETA-GATE).
func Test_INT_001_10_8_new_verified_beta_on_free_five_and_sandbox(t *testing.T) {
	c := cat()
	sandbox := c.Sandbox()

	betaOn := betaReader(t, true).BetaOn(context.Background())
	if !betaOn {
		t.Fatal("precondition: beta_mode=1 in Redis should resolve ON")
	}

	ent := Resolve(nil, false, c) // no subscription row -> Free $5
	if ent.MonthlyIncludedCreditUSD != "5.00" {
		t.Fatalf("free included credit = %q, want \"5.00\"", ent.MonthlyIncludedCreditUSD)
	}

	ceil := Compose(ent, sandbox, betaOn)
	// Beta containment invariant: every axis is held at or below the sandbox
	// ceiling — the user is inside the Sandbox, with no exemption.
	if ceil.RPMMax > sandbox.RPM || ceil.QPSMax > sandbox.QPS || ceil.TPMMax > sandbox.TPM {
		t.Errorf("beta ON ceilings %+v exceed sandbox %+v — user not Beta-gated", ceil, sandbox)
	}
}

// 10.8-INT-002 (P0) — beta_mode=OFF (the default) routes the same new-verified
// Free user to the GA path: Compose returns the plain Free plan ceiling with NO
// sandbox influence (gate-direction correctness).
func Test_INT_002_10_8_beta_off_ga_path(t *testing.T) {
	c := cat()
	sandbox := c.Sandbox()
	free, _ := c.Entitlements(plancatalogue.PlanFree)

	betaOn := betaReader(t, false).BetaOn(context.Background())
	if betaOn {
		t.Fatal("precondition: beta_mode=0 in Redis should resolve OFF")
	}

	ent := Resolve(nil, false, c)
	ceil := Compose(ent, sandbox, betaOn)
	if ceil.RPMMax != free.RPM || ceil.QPSMax != free.QPS || ceil.TPMMax != free.TPM {
		t.Errorf("beta OFF ceilings %+v, want plain Free plan %+v (GA path, not Sandbox)", ceil, free)
	}
}

// 10.8-INT-004 (P0) — zero-money-write: the Beta entitlement path is a pure read.
// Resolve + Compose take only (snapshot, catalogue, sandbox, gate) and return
// ceilings; there is no balances writer, no credit accumulation, and the result
// is deterministic across repeated evaluation (no hidden mutating side effect).
// The static absence of any money-write in the 10.8 changeset is additionally
// enforced by scripts/ci/verify-no-money-path-10.8.sh (UNIT-005 / UNIT-012).
func Test_INT_004_10_8_zero_money_write_pure_read_path(t *testing.T) {
	c := cat()
	sandbox := c.Sandbox()
	betaOn := betaReader(t, true).BetaOn(context.Background())

	first := Compose(Resolve(nil, false, c), sandbox, betaOn)
	for i := 0; i < 8; i++ {
		got := Compose(Resolve(nil, false, c), sandbox, betaOn)
		if got != first {
			t.Fatalf("entitlement read path not idempotent on call %d: %+v != %+v — a side effect leaked", i, got, first)
		}
	}
}
