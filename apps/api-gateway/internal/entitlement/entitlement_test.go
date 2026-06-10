// Tests for the gateway hot-path entitlement resolver (Story 7.8 AC3 + AC2
// composition).
//
// Scenario trace -> docs/qa/assessments/7.8-test-design-20260610.md:
//
//	7.8-UNIT-016  resolve from cached snapshot, NOT a per-request PG read (BR-E-6)
//	7.8-UNIT-017  [SEC] server-side resolution; client-asserted plan IGNORED (BR-E-1)
//	7.8-UNIT-018  [SEC] cache MISS -> FREE ceiling (fail-safe-LOW, BR-E-2)
//	7.8-UNIT-019  [SEC] resolution error/uncertainty -> FREE, never fail-open-high
//	7.8-UNIT-021  corrupt snapshot -> miss -> free
//	7.8-UNIT-022  a Pro snapshot -> Pro ceiling fed to the limiter
//	7.8-UNIT-023  beta ON -> effective = min(plan, sandbox) per axis (BR-B-1)
//	7.8-UNIT-024  NO tier exemption — Enterprise capped at sandbox too (Q-BETA-GATE)
//	7.8-UNIT-025  beta OFF -> plan ceiling stands alone
//	7.8-UNIT-026  min() both directions (plan<sandbox -> plan; sandbox<plan -> sandbox)
//	7.8-INT-016/023  reader cached-read path (miniredis)
package entitlement

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

func cat() plancatalogue.Catalogue { return plancatalogue.DefaultCatalogue }

// 7.8-UNIT-018/019/021 — every uncertain path resolves to the FREE entitlement.
func Test_UNIT_018_resolve_failsafe_low(t *testing.T) {
	free, _ := cat().Entitlements(plancatalogue.PlanFree)

	cases := []struct {
		name  string
		raw   []byte
		found bool
	}{
		{"cache miss (not found)", nil, false},
		{"empty bytes", []byte{}, true},
		{"corrupt snapshot", []byte("{not json"), true},
		{"unknown plan in snapshot", []byte(`{"plan":"galaxy"}`), true},
		{"empty plan field", []byte(`{"plan":""}`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.raw, tc.found, cat())
			if got.Plan != plancatalogue.PlanFree {
				t.Errorf("Resolve(%s) plan = %q, want free (fail-safe-LOW)", tc.name, got.Plan)
			}
			if got.RPM != free.RPM || got.TPM != free.TPM || got.QPS != free.QPS {
				t.Errorf("Resolve(%s) = %+v, want free %+v", tc.name, got, free)
			}
		})
	}
}

// 7.8-UNIT-022 — a valid Pro snapshot resolves to the Pro entitlement.
func Test_UNIT_022_resolve_pro_snapshot(t *testing.T) {
	pro, _ := cat().Entitlements(plancatalogue.PlanPro)
	got := Resolve([]byte(`{"plan":"pro","status":"active"}`), true, cat())
	if got.Plan != plancatalogue.PlanPro || got.RPM != pro.RPM {
		t.Errorf("Resolve(pro) = %+v, want pro %+v", got, pro)
	}
}

// 7.8-UNIT-017 [SEC] — Resolve takes ONLY the snapshot + catalogue; there is no
// request-field input, so a client-asserted plan cannot influence resolution.
// A free-snapshot user stays free regardless of any (hypothetical) higher hint.
func Test_UNIT_017_client_asserted_plan_ignored(t *testing.T) {
	// The snapshot says free; the catalogue resolves free. There is no API on
	// Resolve to inject a client plan — that is the structural guarantee.
	got := Resolve([]byte(`{"plan":"free"}`), true, cat())
	if got.Plan != plancatalogue.PlanFree {
		t.Fatalf("free snapshot resolved to %q", got.Plan)
	}
}

// 7.8-UNIT-023/024/025/026 — Compose: min(plan, sandbox) per axis with no
// exemption when beta ON; plan alone when OFF.
func Test_UNIT_023_compose_beta_min(t *testing.T) {
	c := cat()
	sandbox := c.Sandbox()                                 // rpm=60, qps=2, tpm=120000
	ent, _ := c.Entitlements(plancatalogue.PlanEnterprise) // rpm=10000, qps=50, tpm=20,000,000

	// beta OFF -> plan stands alone (UNIT-025).
	off := Compose(ent, sandbox, false)
	if off.RPMMax != ent.RPM || off.QPSMax != ent.QPS || off.TPMMax != ent.TPM {
		t.Errorf("beta OFF: got %+v, want plan ceiling %+v", off, ent)
	}

	// beta ON -> min per axis; Enterprise is NOT exempt (UNIT-023/024).
	on := Compose(ent, sandbox, true)
	if on.RPMMax != sandbox.RPM { // 60 < 10000
		t.Errorf("beta ON rpm = %d, want sandbox %d (no Enterprise exemption)", on.RPMMax, sandbox.RPM)
	}
	if on.QPSMax != sandbox.QPS { // 2 < 50
		t.Errorf("beta ON qps = %d, want sandbox %d", on.QPSMax, sandbox.QPS)
	}
	if on.TPMMax != sandbox.TPM { // 120000 < 20,000,000
		t.Errorf("beta ON tpm = %d, want sandbox %d", on.TPMMax, sandbox.TPM)
	}
}

// 7.8-UNIT-026 — min() picks the smaller per axis in BOTH directions.
func Test_UNIT_026_compose_min_both_directions(t *testing.T) {
	// A synthetic entitlement whose qps is BELOW the sandbox but whose rpm is
	// ABOVE it: min must pick plan-qps and sandbox-rpm.
	ent := plancatalogue.Entitlement{Plan: plancatalogue.PlanFree, RPM: 1000, TPM: 1000, QPS: 1}
	sandbox := plancatalogue.SandboxCeiling{RPM: 60, TPM: 60, QPS: 50}
	got := Compose(ent, sandbox, true)
	if got.QPSMax != 1 { // plan < sandbox -> plan wins
		t.Errorf("qps min = %d, want 1 (plan<sandbox)", got.QPSMax)
	}
	if got.RPMMax != 60 { // sandbox < plan -> sandbox wins
		t.Errorf("rpm min = %d, want 60 (sandbox<plan)", got.RPMMax)
	}
}

// 7.8-INT-016/023 — the Resolver does a single cached Redis GET and composes;
// no PG/billing call. A Pro snapshot -> Pro ceiling; a missing key -> free.
func Test_INT_016_resolver_cached_read(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const userID = "u-pro"
	snap, _ := Snapshot{Plan: "pro", Status: "active"}.Marshal()
	mr.Set(SnapshotKey(userID), string(snap))

	r := NewResolver(rdb, cat(), nil /* beta off */, nil)
	pro, _ := cat().Entitlements(plancatalogue.PlanPro)
	got := r.CeilingsForUser(context.Background(), userID)
	if got.RPMMax != pro.RPM {
		t.Errorf("resolver pro rpm = %d, want %d", got.RPMMax, pro.RPM)
	}

	// 7.8-UNIT-018 INT companion: a user with no snapshot -> free.
	free, _ := cat().Entitlements(plancatalogue.PlanFree)
	gotFree := r.CeilingsForUser(context.Background(), "u-nobody")
	if gotFree.RPMMax != free.RPM {
		t.Errorf("resolver miss rpm = %d, want free %d (fail-safe-LOW)", gotFree.RPMMax, free.RPM)
	}
}

// 7.8-UNIT-016 — the resolver reads the owner user_id from context (server-side)
// via the bearer-auth helper, not the apiKeyID argument; a free-snapshot user is
// throttled at free even with a (here irrelevant) api key id.
func Test_UNIT_016_resolve_ceilings_reads_context_user(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const userID = "u-ent"
	snap, _ := Snapshot{Plan: "enterprise", Status: "active"}.Marshal()
	mr.Set(SnapshotKey(userID), string(snap))

	r := NewResolver(rdb, cat(), nil, nil)
	fn := r.ResolveCeilings()

	// With the owner user_id in context, the enterprise ceiling resolves.
	ctx := middleware.BearerWithUserID(context.Background(), userID)
	ent, _ := cat().Entitlements(plancatalogue.PlanEnterprise)
	got, err := fn(ctx, "any-api-key-id")
	if err != nil {
		t.Fatalf("ResolveCeilings err = %v", err)
	}
	if got.RPMMax != ent.RPM {
		t.Errorf("ctx-user resolve rpm = %d, want enterprise %d", got.RPMMax, ent.RPM)
	}

	// With NO user in context -> free (fail-safe-LOW), regardless of api key id.
	free, _ := cat().Entitlements(plancatalogue.PlanFree)
	gotNoUser, _ := fn(context.Background(), "any-api-key-id")
	if gotNoUser.RPMMax != free.RPM {
		t.Errorf("no-ctx-user resolve rpm = %d, want free %d", gotNoUser.RPMMax, free.RPM)
	}
}
