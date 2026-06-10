// Tests for billing-svc subscription plan-change orchestration (Story 7.8 AC1)
// + the entitlement snapshot writer (AC3, BR-E-3).
//
// Scenario trace -> docs/qa/assessments/7.8-test-design-20260610.md:
//
//	7.8-UNIT-008  ChangePlan validates P_new against catalogue; unknown -> reject (BR-S-1)
//	7.8-UNIT-009  upgrade (rank up) -> immediate provider call
//	7.8-UNIT-010  downgrade (rank down) -> deferred-to-period-end
//	7.8-UNIT-011  same-plan -> idempotent no-op, NO provider call
//	7.8-UNIT-013  subscriptions.plan NOT optimistically mutated (no PG write here; webhook confirms)
//	7.8-UNIT-014  upgrade writes the optimistic entitlement snapshot
//	7.8-UNIT-015  NO proration credit; balances untouched (structural — no balance surface)
//	7.8-INT-016/017/020  snapshot writer: sole-writer, sentinel, TTL <= 60s
package subscription

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	plancatalogue "github.com/he-api/he-api/packages/plan-catalogue"
)

// --- fakes ---

type fakeSubReader struct {
	sub   Subscription
	found bool
	err   error
}

func (f fakeSubReader) CurrentSubscription(context.Context, string) (Subscription, bool, error) {
	return f.sub, f.found, f.err
}

type fakeProvider struct {
	calls       int
	lastPlan    string
	lastProrate bool
	lastDefer   bool
	err         error
}

func (f *fakeProvider) UpdateProviderSubscription(_ context.Context, _, _, newPlan string, prorate, atPeriodEnd bool) error {
	f.calls++
	f.lastPlan = newPlan
	f.lastProrate = prorate
	f.lastDefer = atPeriodEnd
	return f.err
}

type fakeSnapshot struct {
	written  int
	lastPlan plancatalogue.PlanKey
}

func (f *fakeSnapshot) WriteActive(_ context.Context, _ string, plan plancatalogue.PlanKey) error {
	f.written++
	f.lastPlan = plan
	return nil
}

func cat() plancatalogue.Catalogue { return plancatalogue.DefaultCatalogue }

// 7.8-UNIT-005 echo — ResolveDirection by rank, with default-free for an absent
// current plan.
func Test_ResolveDirection(t *testing.T) {
	c := cat()
	cases := []struct {
		old, new plancatalogue.PlanKey
		want     Direction
		wantErr  bool
	}{
		{plancatalogue.PlanFree, plancatalogue.PlanPro, DirectionUpgrade, false},
		{plancatalogue.PlanPro, plancatalogue.PlanFree, DirectionDowngrade, false},
		{plancatalogue.PlanTeam, plancatalogue.PlanTeam, DirectionSame, false},
		{"", plancatalogue.PlanEnterprise, DirectionUpgrade, false}, // absent current -> free
		{plancatalogue.PlanPro, "galaxy", DirectionSame, true},      // unknown new -> err
	}
	for _, tc := range cases {
		got, err := ResolveDirection(c, tc.old, tc.new)
		if tc.wantErr && err == nil {
			t.Errorf("ResolveDirection(%q->%q) err=nil, want ErrUnknownPlan", tc.old, tc.new)
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ResolveDirection(%q->%q) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

// 7.8-UNIT-008 — unknown plan rejected with NO provider call, NO snapshot write.
func Test_UNIT_008_unknown_plan_rejected(t *testing.T) {
	prov := &fakeProvider{}
	snap := &fakeSnapshot{}
	svc := NewService(cat(), fakeSubReader{found: false}, prov, snap, nil)
	_, err := svc.ChangePlan(context.Background(), "u1", "galaxy")
	if err != ErrUnknownPlan {
		t.Fatalf("err = %v, want ErrUnknownPlan", err)
	}
	if prov.calls != 0 || snap.written != 0 {
		t.Errorf("unknown plan must not call provider (%d) or write snapshot (%d)", prov.calls, snap.written)
	}
}

// 7.8-UNIT-009 + UNIT-014 — upgrade calls the provider immediately WITH proration
// and writes the optimistic snapshot.
func Test_UNIT_009_upgrade_immediate(t *testing.T) {
	prov := &fakeProvider{}
	snap := &fakeSnapshot{}
	svc := NewService(cat(),
		fakeSubReader{found: true, sub: Subscription{Plan: plancatalogue.PlanFree, Status: "active", Provider: "stripe", ExternalSubscriptionID: "sub_1"}},
		prov, snap, nil)
	res, err := svc.ChangePlan(context.Background(), "u1", plancatalogue.PlanPro)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Direction != DirectionUpgrade || res.DeferredToPeriodEnd {
		t.Errorf("res = %+v, want immediate upgrade", res)
	}
	if prov.calls != 1 || !prov.lastProrate || prov.lastDefer {
		t.Errorf("upgrade must call provider once with proration, no defer: %+v", prov)
	}
	if snap.written != 1 || snap.lastPlan != plancatalogue.PlanPro {
		t.Errorf("upgrade must optimistically write Pro snapshot: %+v", snap)
	}
}

// 7.8-UNIT-010 — downgrade defers to period end (no proration), NO optimistic
// snapshot (the higher tier is kept until the roll).
func Test_UNIT_010_downgrade_deferred(t *testing.T) {
	prov := &fakeProvider{}
	snap := &fakeSnapshot{}
	svc := NewService(cat(),
		fakeSubReader{found: true, sub: Subscription{Plan: plancatalogue.PlanPro, Status: "active", Provider: "stripe", ExternalSubscriptionID: "sub_1"}},
		prov, snap, nil)
	res, err := svc.ChangePlan(context.Background(), "u1", plancatalogue.PlanFree)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Direction != DirectionDowngrade || !res.DeferredToPeriodEnd {
		t.Errorf("res = %+v, want deferred downgrade", res)
	}
	if prov.calls != 1 || prov.lastProrate || !prov.lastDefer {
		t.Errorf("downgrade must call provider once, no proration, at period end: %+v", prov)
	}
	if snap.written != 0 {
		t.Errorf("downgrade must NOT optimistically change the snapshot (keep higher until roll): %d", snap.written)
	}
}

// 7.8-UNIT-011 — same plan is an idempotent no-op: NO provider call, NO snapshot.
func Test_UNIT_011_same_plan_noop(t *testing.T) {
	prov := &fakeProvider{}
	snap := &fakeSnapshot{}
	svc := NewService(cat(),
		fakeSubReader{found: true, sub: Subscription{Plan: plancatalogue.PlanTeam, Status: "active"}},
		prov, snap, nil)
	res, err := svc.ChangePlan(context.Background(), "u1", plancatalogue.PlanTeam)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Direction != DirectionSame || res.ProviderCalled {
		t.Errorf("same plan res = %+v, want no-op", res)
	}
	if prov.calls != 0 || snap.written != 0 {
		t.Errorf("same plan must not call provider (%d) or write snapshot (%d)", prov.calls, snap.written)
	}
}

// 7.8-INT-016/017/020 — the snapshot writer is the sole writer; WriteActive sets
// a key with TTL ≤ 60s and clears the sentinel; Invalidate deletes the snapshot
// and sets a sentinel.
func Test_INT_016_snapshot_writer(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewSnapshotWriter(rdb, nil)
	ctx := context.Background()
	const uid = "u-1"

	if err := w.WriteActive(ctx, uid, plancatalogue.PlanPro); err != nil {
		t.Fatalf("WriteActive: %v", err)
	}
	got, err := mr.Get(snapshotKey(uid))
	if err != nil || got == "" {
		t.Fatalf("snapshot not written: %v %q", err, got)
	}
	// TTL must be bounded ≤ 60s (Q-ENTITLEMENT-ENFORCE).
	ttl := mr.TTL(snapshotKey(uid))
	if ttl <= 0 || ttl > 60*time.Second {
		t.Errorf("snapshot TTL = %v, want (0, 60s]", ttl)
	}

	// Invalidate deletes the snapshot and sets a sentinel.
	if err := w.Invalidate(ctx, uid); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if mr.Exists(snapshotKey(uid)) {
		t.Errorf("snapshot must be deleted on Invalidate")
	}
	if !mr.Exists(sentinelKey(uid)) {
		t.Errorf("invalidation sentinel must be set")
	}
}
