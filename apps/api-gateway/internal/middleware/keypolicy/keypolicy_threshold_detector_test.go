// Story 5.4 — 5.4-UNIT-011..020 (threshold-crossing detector: 80% warning,
// both-crossings, cap-NULL skip, boundary + named-constant assertion).
package keypolicy_test

import (
	"net/http"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
	"github.com/he-api/he-api/apps/api-gateway/internal/notifyclient"
)

// UNIT-011: below 0.80 → no fire (already covered by under-cap passthrough,
// asserted here against the notifier directly at 0.79·cap).
func TestThreshold_BelowWarning_NoFire(t *testing.T) {
	notif := &fakeNotifier{}
	opts := keypolicy.Options{
		CapSentinel: &fakeSentinel{},
		CostReader:  (&countingReader{cur: "39.50"}).read, // 0.79 of 50
		Notifier:    notif,
	}
	_, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)
	if !next {
		t.Fatal("under cap must pass through")
	}
	if len(notif.fires) != 0 {
		t.Fatalf("below 80%% must not fire; got %v", notif.fires)
	}
}

// UNIT-012: exactly 0.80 → fire WARNING_80 only; assert the named constant.
func TestThreshold_ExactlyWarning_FiresWarningOnly(t *testing.T) {
	if keypolicy.MonthlyCapWarningThresholdRatio != 0.80 {
		t.Fatalf("MonthlyCapWarningThresholdRatio=%v want 0.80 (m-2)", keypolicy.MonthlyCapWarningThresholdRatio)
	}
	if keypolicy.MonthlyCapTrippedThresholdRatio != 1.00 {
		t.Fatalf("MonthlyCapTrippedThresholdRatio=%v want 1.00 (m-2)", keypolicy.MonthlyCapTrippedThresholdRatio)
	}
	notif := &fakeNotifier{}
	opts := keypolicy.Options{
		CapSentinel: &fakeSentinel{},
		CostReader:  (&countingReader{cur: "40.00"}).read, // exactly 0.80 of 50
		Notifier:    notif,
	}
	_, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)
	if !next {
		t.Fatal("at 0.80·cap (still < cap) must pass through")
	}
	if len(notif.fires) != 1 || notif.fires[0] != notifyclient.ThresholdWarning80 {
		t.Fatalf("want exactly [WARNING_80]; got %v", notif.fires)
	}
}

// UNIT-013: crossing both 0.80 and 1.00 in one read (winner) → both fire.
func TestThreshold_BothCrossings_WinnerFiresBoth(t *testing.T) {
	notif := &fakeNotifier{}
	opts := keypolicy.Options{
		CapSentinel: &fakeSentinel{setnxResult: true}, // wins the trip race
		CostReader:  (&countingReader{cur: "55.00"}).read,
		Notifier:    notif,
	}
	w, _ := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("over cap must 402; got %d", w.Code)
	}
	if !containsThreshold(notif.fires, notifyclient.ThresholdWarning80) ||
		!containsThreshold(notif.fires, notifyclient.ThresholdTripped) {
		t.Fatalf("winner must fire both WARNING_80 + TRIPPED; got %v", notif.fires)
	}
}

// UNIT-014: cap = NULL → detector skipped entirely (BR-2.13).
func TestThreshold_NilCap_Skipped(t *testing.T) {
	notif := &fakeNotifier{}
	claims := &middleware.CachedClaims{APIKeyID: "k1"} // no cap
	opts := keypolicy.Options{
		CapSentinel: &fakeSentinel{existsResult: true}, // would 402 if consulted
		CostReader:  (&countingReader{cur: "999.00"}).read,
		Notifier:    notif,
	}
	_, next := run(t, claims, opts, `{"model":"qwen-max"}`)
	if !next {
		t.Fatal("nil cap must skip the whole cap branch (no sentinel probe)")
	}
	if len(notif.fires) != 0 {
		t.Fatalf("nil cap must not fire; got %v", notif.fires)
	}
}

// UNIT-016..018: defensive boundaries — zero/negative cap → no fire, no panic.
func TestThreshold_DefensiveBoundaries(t *testing.T) {
	for _, cap := range []string{"0", "0.00", "-1.00", "notanumber"} {
		notif := &fakeNotifier{}
		opts := keypolicy.Options{
			CapSentinel: &fakeSentinel{},
			CostReader:  (&countingReader{cur: "10.00"}).read,
			Notifier:    notif,
		}
		// cap="0"/"-1"/"notanumber": CheckMonthlyCap fails-open (allowed),
		// crossedWarning returns false → no fire, no panic.
		_, _ = run(t, capClaims(cap), opts, `{"model":"qwen-max"}`)
		if len(notif.fires) != 0 {
			t.Fatalf("cap=%q must not fire; got %v", cap, notif.fires)
		}
	}
}
