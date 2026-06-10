// Story 7.7 AC2 — low-balance alert dedupe + episode + variant tests.
// 7.7-INT-050 once-per-episode dedupe, 7.7-INT-051 episode clear on recovery,
// 7.7-INT-053 failed variant, 7.7-INT-052 suppression (AlertNone → no send),
// 7.7-BLIND-ERROR-003 fire-and-forget (notifier down → sentinel rolled back).
package lowbalance

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/billing-svc/internal/autorecharge"
)

type sendCall struct {
	userID, slug, balance, threshold string
}

type fakeNotifier struct {
	calls []sendCall
	err   error
}

func (f *fakeNotifier) SendLowBalance(_ context.Context, userID, slug, balance, threshold string) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, sendCall{userID, slug, balance, threshold})
	return nil
}

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

// 7.7-INT-050 — many sub-threshold debits send EXACTLY ONE email per episode.
func TestHandle_OncePerEpisode(t *testing.T) {
	ctx := context.Background()
	n := &fakeNotifier{}
	a := New(newRedis(t), n, nil)
	r := autorecharge.Result{Alert: autorecharge.AlertLowBalance, Balance: "4.50", Threshold: "5.00"}
	for i := 0; i < 5; i++ {
		a.Handle(ctx, "u1", r)
	}
	if len(n.calls) != 1 {
		t.Fatalf("sent %d emails, want exactly 1 per episode", len(n.calls))
	}
	if n.calls[0].slug != TemplateLowBalance || n.calls[0].balance != "4.50" {
		t.Fatalf("unexpected call: %+v", n.calls[0])
	}
}

// 7.7-INT-051 — the sentinel clears when the balance recovers above threshold; a
// later dip re-alerts exactly once.
func TestHandle_EpisodeClearOnRecovery(t *testing.T) {
	ctx := context.Background()
	n := &fakeNotifier{}
	a := New(newRedis(t), n, nil)
	low := autorecharge.Result{Alert: autorecharge.AlertLowBalance, Balance: "4.50", Threshold: "5.00"}
	a.Handle(ctx, "u1", low) // dip → 1 email
	// Recovery: a successful recharge lifts the balance above threshold → AlertNone.
	a.Handle(ctx, "u1", autorecharge.Result{Alert: autorecharge.AlertNone, Balance: "24.50", Threshold: "5.00"})
	a.Handle(ctx, "u1", low) // re-dip → a NEW episode → 1 more email
	if len(n.calls) != 2 {
		t.Fatalf("sent %d emails, want 2 (one per episode)", len(n.calls))
	}
}

// 7.7-INT-053 — the "auto-recharge failed" variant fires with its own template.
func TestHandle_FailedVariant(t *testing.T) {
	ctx := context.Background()
	n := &fakeNotifier{}
	a := New(newRedis(t), n, nil)
	a.Handle(ctx, "u1", autorecharge.Result{Alert: autorecharge.AlertFailed, Balance: "4.50", Threshold: "5.00"})
	if len(n.calls) != 1 || n.calls[0].slug != TemplateLowBalanceFailed {
		t.Fatalf("want one low_balance_failed email, got %+v", n.calls)
	}
}

// 7.7-INT-052 — AlertNone (auto-recharge succeeded / above threshold) → NO email.
func TestHandle_SuppressedNoSend(t *testing.T) {
	ctx := context.Background()
	n := &fakeNotifier{}
	a := New(newRedis(t), n, nil)
	// Suppressed (triggered, balance still below threshold pending top-up).
	a.Handle(ctx, "u1", autorecharge.Result{Alert: autorecharge.AlertNone, Balance: "4.50", Threshold: "5.00"})
	if len(n.calls) != 0 {
		t.Fatalf("suppressed alert still sent %d emails", len(n.calls))
	}
}

// 7.7-BLIND-ERROR-003 — notifier down → the sentinel is rolled back so a later
// attempt can still alert (fire-and-forget never blocks the committed debit).
func TestHandle_NotifierDownRollsBackSentinel(t *testing.T) {
	ctx := context.Background()
	down := &fakeNotifier{err: errors.New("notification-svc unavailable")}
	rdb := newRedis(t)
	a := New(rdb, down, nil)
	r := autorecharge.Result{Alert: autorecharge.AlertLowBalance, Balance: "4.50", Threshold: "5.00"}
	a.Handle(ctx, "u1", r)
	// The sentinel must NOT remain set (so the next debit re-attempts).
	if rdb.Exists(ctx, episodeKey("u1")).Val() != 0 {
		t.Fatalf("sentinel left set after a failed send — a later alert would be lost")
	}
	// Recovery from the outage: a working notifier now succeeds.
	up := &fakeNotifier{}
	a2 := New(rdb, up, nil)
	a2.Handle(ctx, "u1", r)
	if len(up.calls) != 1 {
		t.Fatalf("alert not re-attempted after the notifier recovered")
	}
}
