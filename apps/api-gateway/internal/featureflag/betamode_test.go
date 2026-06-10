// Tests for the gateway Beta-mode reader (Story 7.8 AC2).
//
// Scenario trace -> docs/qa/assessments/7.8-test-design-20260610.md:
//
//	7.8-UNIT-027  Redis flag:beta_mode is the runtime read source; PG is cold-start fallback (BR-B-2)
//	7.8-UNIT-028  [SAFETY] booting pod, no Redis AND no PG -> beta OFF (cold-infra; do not trap users)
//	7.8-UNIT-029  [SAFETY] running pod loses Redis -> holds last-known until reconcile (distinct direction)
//	7.8-BLIND-ERROR-004  Redis read down -> PG fallback; PG also down on boot -> OFF
package featureflag

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

// 7.8-UNIT-027 — Redis is the runtime read source. ON/OFF in Redis is honoured
// without ever consulting PG (the cold-start reader must NOT be called on a
// Redis hit).
func Test_UNIT_027_redis_is_runtime_source(t *testing.T) {
	mr, rdb := newRedis(t)
	coldCalled := false
	cold := func(context.Context) (bool, bool, error) { coldCalled = true; return false, true, nil }
	r := NewReader(rdb, cold, nil)

	mr.Set(BetaModeRedisKey, "1")
	if !r.BetaOn(context.Background()) {
		t.Errorf("beta should be ON from Redis=1")
	}
	mr.Set(BetaModeRedisKey, "0")
	if r.BetaOn(context.Background()) {
		t.Errorf("beta should be OFF from Redis=0")
	}
	if coldCalled {
		t.Errorf("cold-start PG reader was consulted despite a Redis hit")
	}
}

// 7.8-UNIT-028 [SAFETY] — a BOOTING pod (no last-known) with NO Redis answer AND
// NO PG signal resolves to OFF: a cold infra fault must not trap everyone in the
// sandbox.
func Test_UNIT_028_booting_no_signal_off(t *testing.T) {
	t.Run("no redis key + PG absent -> OFF", func(t *testing.T) {
		_, rdb := newRedis(t)                                                          // key never set -> redis.Nil
		cold := func(context.Context) (bool, bool, error) { return false, false, nil } // PG row absent
		r := NewReader(rdb, cold, nil)
		if r.BetaOn(context.Background()) {
			t.Errorf("booting + no signal should be OFF")
		}
	})
	t.Run("redis down + PG down -> OFF (BLIND-ERROR-004)", func(t *testing.T) {
		mr, rdb := newRedis(t)
		mr.Close() // Redis unreachable
		cold := func(context.Context) (bool, bool, error) { return false, false, errors.New("pg down") }
		r := NewReader(rdb, cold, nil)
		if r.BetaOn(context.Background()) {
			t.Errorf("booting + redis-down + pg-down should fail safe to OFF")
		}
	})
	t.Run("no redis + no cold reader -> OFF", func(t *testing.T) {
		_, rdb := newRedis(t)
		r := NewReader(rdb, nil, nil)
		if r.BetaOn(context.Background()) {
			t.Errorf("booting + no PG fallback should be OFF")
		}
	})
}

// 7.8-UNIT-028 companion — a booting pod with no Redis but a PG row present
// resolves from the PG cold-start SoT.
func Test_UNIT_028b_booting_uses_pg_cold_start(t *testing.T) {
	_, rdb := newRedis(t) // no runtime key
	cold := func(context.Context) (bool, bool, error) { return true, true, nil }
	r := NewReader(rdb, cold, nil)
	if !r.BetaOn(context.Background()) {
		t.Errorf("booting pod should read ON from the PG cold-start row")
	}
}

// 7.8-UNIT-029 [SAFETY] — a RUNNING pod (already resolved ON from Redis) that
// then LOSES Redis holds the last-known ON value, rather than flipping OFF.
// Distinct from the cold-infra direction (UNIT-028).
func Test_UNIT_029_running_loses_redis_holds_last_known(t *testing.T) {
	mr, rdb := newRedis(t)
	// PG would say OFF — proving we hold last-known, NOT re-read PG, on Redis loss.
	cold := func(context.Context) (bool, bool, error) { return false, true, nil }
	r := NewReader(rdb, cold, nil)

	mr.Set(BetaModeRedisKey, "1")
	if !r.BetaOn(context.Background()) {
		t.Fatalf("precondition: should resolve ON from Redis")
	}

	mr.Close() // running pod loses Redis
	if !r.BetaOn(context.Background()) {
		t.Errorf("running pod that lost Redis must HOLD last-known ON, not flip OFF or re-read PG")
	}
}

func Test_parseBool(t *testing.T) {
	on := []string{"1", "true", "TRUE", "on", " enabled ", "yes", "t"}
	off := []string{"0", "false", "", "off", "nope", "2"}
	for _, s := range on {
		if !parseBool(s) {
			t.Errorf("parseBool(%q) = false, want true", s)
		}
	}
	for _, s := range off {
		if parseBool(s) {
			t.Errorf("parseBool(%q) = true, want false", s)
		}
	}
}
