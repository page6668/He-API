// Story 5.4 — 5.4-UNIT-001..010 (sticky-trip sentinel fast path + first-cross
// SETNX-gated fire + fail-OPEN). Drives the keypolicy middleware through its
// AC1 branches with recording fakes for the sentinel + notifier.
package keypolicy_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
	"github.com/he-api/he-api/apps/api-gateway/internal/notifyclient"
)

// --- recording fakes -------------------------------------------------------

type fakeSentinel struct {
	existsResult bool
	existsErr    error
	setnxResult  bool
	setnxErr     error
	existsCalls  int
	setnxCalls   int
}

func (f *fakeSentinel) Exists(context.Context, string) (bool, error) {
	f.existsCalls++
	return f.existsResult, f.existsErr
}

func (f *fakeSentinel) SetNX(context.Context, string) (bool, error) {
	f.setnxCalls++
	return f.setnxResult, f.setnxErr
}

type fakeNotifier struct{ fires []notifyclient.Threshold }

func (f *fakeNotifier) NotifyCapThresholdAsync(_ context.Context, _ string, t notifyclient.Threshold) {
	f.fires = append(f.fires, t)
}

// countingReader records how many counter GETs the middleware performs, so the
// sentinel-hit fast path can be asserted to skip the GET (BR-1.1).
type countingReader struct {
	cur   string
	err   error
	calls int
}

func (c *countingReader) read(context.Context, string) (string, bool, error) {
	c.calls++
	return c.cur, c.cur != "", c.err
}

func capClaims(cap string) *middleware.CachedClaims {
	return &middleware.CachedClaims{APIKeyID: "k-cap", UserID: "u1", MonthlyCostCapUSD: capPtr(cap)}
}

// --- UNIT-001: sentinel hit → 402, no counter GET --------------------------

func TestCapTripped_SentinelHit_FastPath(t *testing.T) {
	sent := &fakeSentinel{existsResult: true}
	cr := &countingReader{cur: "10.00"} // well under cap; must NOT be read
	notif := &fakeNotifier{}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: notif}

	w, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if w.Code != http.StatusPaymentRequired || !strings.Contains(w.Body.String(), "402_quota_exhausted") {
		t.Fatalf("status=%d body=%s want 402 quota_exhausted", w.Code, w.Body.String())
	}
	if next {
		t.Fatal("sentinel hit must short-circuit")
	}
	if cr.calls != 0 {
		t.Fatalf("counter GET ran %d times on sentinel hit — must be 0 (BR-1.1)", cr.calls)
	}
	if sent.existsCalls != 1 {
		t.Fatalf("Exists calls=%d want 1", sent.existsCalls)
	}
	if len(notif.fires) != 0 {
		t.Fatalf("sentinel hit fired %v notifications — must be none (BR-1.11)", notif.fires)
	}
}

// --- UNIT-002: sentinel miss + current<cap → passthrough -------------------

func TestCapTripped_SentinelMiss_UnderCap_Passthrough(t *testing.T) {
	sent := &fakeSentinel{existsResult: false}
	cr := &countingReader{cur: "10.00"}
	notif := &fakeNotifier{}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: notif}

	_, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if !next {
		t.Fatal("under cap must pass through")
	}
	if cr.calls != 1 {
		t.Fatalf("counter GET calls=%d want 1 (slow path)", cr.calls)
	}
	if len(notif.fires) != 0 {
		t.Fatalf("under 80%% must not fire: %v", notif.fires)
	}
}

// --- UNIT-003: sentinel miss + current>=cap → SETNX win, fire, 402 ---------

func TestCapTripped_SlowPathFirstCross_SetNX_Fire_402(t *testing.T) {
	sent := &fakeSentinel{existsResult: false, setnxResult: true}
	cr := &countingReader{cur: "55.00"}
	notif := &fakeNotifier{}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: notif}

	w, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if w.Code != http.StatusPaymentRequired || next {
		t.Fatalf("status=%d next=%v want 402 short-circuit", w.Code, next)
	}
	if sent.setnxCalls != 1 {
		t.Fatalf("SETNX calls=%d want 1", sent.setnxCalls)
	}
	if !containsThreshold(notif.fires, notifyclient.ThresholdTripped) {
		t.Fatalf("winner must fire TRIPPED; got %v", notif.fires)
	}
}

// --- UNIT-004: SETNX lost (concurrent) → 402, NO fire ----------------------

func TestCapTripped_SlowPathFirstCross_SetNXLost_NoFire(t *testing.T) {
	sent := &fakeSentinel{existsResult: false, setnxResult: false} // lost the race
	cr := &countingReader{cur: "55.00"}
	notif := &fakeNotifier{}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: notif}

	w, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if w.Code != http.StatusPaymentRequired || next {
		t.Fatalf("status=%d next=%v want 402 short-circuit", w.Code, next)
	}
	if len(notif.fires) != 0 {
		t.Fatalf("SETNX loser must not fire (BR-1.8/1.9 dedup); got %v", notif.fires)
	}
}

// --- UNIT-005: sentinel EXISTS error → fall through (fail-OPEN) -------------

func TestCapTripped_SentinelExistsError_FailOpen(t *testing.T) {
	sent := &fakeSentinel{existsErr: errors.New("redis down"), setnxResult: true}
	cr := &countingReader{cur: "10.00"} // under cap → passthrough after fall-through
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: &fakeNotifier{}}

	_, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if !next {
		t.Fatal("Exists error must fall through to counter slow path (BR-1.10 fail-OPEN)")
	}
	if cr.calls != 1 {
		t.Fatalf("fail-open must reach counter GET; calls=%d", cr.calls)
	}
}

// --- UNIT-006: 402 envelope shape matches §5.1.2 ---------------------------

func TestCapTripped_EnvelopeShape(t *testing.T) {
	sent := &fakeSentinel{existsResult: true}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: (&countingReader{}).read}
	w, _ := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	body := w.Body.String()
	for _, field := range []string{`"code":"402_quota_exhausted"`, `"message"`, `"type"`, `"param"`, `"he_request_id"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("envelope missing %s; body=%s", field, body)
		}
	}
}

// --- UNIT-005b: SETNX write error on first cross → 402 anyway, no fire ------

func TestCapTripped_SetNXError_402_NoFire(t *testing.T) {
	sent := &fakeSentinel{existsResult: false, setnxErr: errors.New("redis down")}
	cr := &countingReader{cur: "55.00"}
	notif := &fakeNotifier{}
	opts := keypolicy.Options{CapSentinel: sent, CostReader: cr.read, Notifier: notif}

	w, next := run(t, capClaims("50.00"), opts, `{"model":"qwen-max"}`)

	if w.Code != http.StatusPaymentRequired || next {
		t.Fatalf("status=%d next=%v want 402 (cap IS exceeded regardless of SETNX)", w.Code, next)
	}
	if len(notif.fires) != 0 {
		t.Fatalf("SETNX error → skip fire (fail-OPEN, avoid per-request dupes); got %v", notif.fires)
	}
}

func containsThreshold(fires []notifyclient.Threshold, want notifyclient.Threshold) bool {
	for _, f := range fires {
		if f == want {
			return true
		}
	}
	return false
}
