// Story 6.5 — account-level user-default precedence tier + Decider wiring.
package routingclient_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

const (
	UNSPEC  = routingv1.Strategy_STRATEGY_UNSPECIFIED
	DEFAULT = routingv1.Strategy_STRATEGY_DEFAULT
	QUALITY = routingv1.Strategy_STRATEGY_QUALITY
	COST    = routingv1.Strategy_STRATEGY_COST
	LATENCY = routingv1.Strategy_STRATEGY_LATENCY
)

// 6.5-UNIT-016 — default + header → header wins (user-default ignored).
func TestParseStrategy_DefaultPlusHeader_HeaderWins(t *testing.T) {
	t.Parallel()
	s, req, isMeta, _ := routingclient.ParseStrategy("gpt-4o", hdr(routingclient.RoutingStrategyHeader, "quality"), COST)
	if s != QUALITY || req != "gpt-4o" || isMeta {
		t.Errorf("got (%v,%q,meta=%v), want (QUALITY,gpt-4o,false) — header outranks user-default", s, req, isMeta)
	}
}

// 6.5-UNIT-017 — default + meta-model → meta wins.
func TestParseStrategy_DefaultPlusMeta_MetaWins(t *testing.T) {
	t.Parallel()
	s, req, isMeta, _ := routingclient.ParseStrategy("he-router-latency", http.Header{}, COST)
	if s != LATENCY || req != "" || !isMeta {
		t.Errorf("got (%v,%q,meta=%v), want (LATENCY,\"\",true) — meta outranks user-default", s, req, isMeta)
	}
}

// 6.5-UNIT-018 — concrete model, no header/meta, default=cost → STRATEGY_COST.
func TestParseStrategy_DefaultOnly_RoutesByUserDefault(t *testing.T) {
	t.Parallel()
	s, req, isMeta, conflict := routingclient.ParseStrategy("gpt-4o", http.Header{}, COST)
	if s != COST || req != "gpt-4o" || isMeta || conflict {
		t.Errorf("got (%v,%q,meta=%v,conflict=%v), want (COST,gpt-4o,false,false)", s, req, isMeta, conflict)
	}
}

// 6.5-UNIT-019 — no default, no header, no meta → STRATEGY_DEFAULT,
// requested==model (BYTE-FOR-BYTE Story 6.2; zero-regression lock).
func TestParseStrategy_NoDefault_ByteForBye62(t *testing.T) {
	t.Parallel()
	s, req, isMeta, conflict := routingclient.ParseStrategy("gpt-4o", http.Header{}, UNSPEC)
	if s != DEFAULT || req != "gpt-4o" || isMeta || conflict {
		t.Errorf("got (%v,%q,meta=%v,conflict=%v), want (DEFAULT,gpt-4o,false,false)", s, req, isMeta, conflict)
	}
}

// 6.5-UNIT-020 — meta + header + default all present → meta wins; conflict still
// set (header present → 6.2 conflict-WARN preserved).
func TestParseStrategy_MetaHeaderDefault_MetaWinsConflictSet(t *testing.T) {
	t.Parallel()
	s, req, isMeta, conflict := routingclient.ParseStrategy("he-router-quality", hdr(routingclient.RoutingStrategyHeader, "cost"), LATENCY)
	if s != QUALITY || req != "" || !isMeta || !conflict {
		t.Errorf("got (%v,%q,meta=%v,conflict=%v), want (QUALITY,\"\",true,true)", s, req, isMeta, conflict)
	}
}

// 6.5-UNIT-021 — enum→Strategy map: each persisted strategy drives; UNSPECIFIED
// skips the tier (→ DEFAULT).
func TestParseStrategy_UserDefaultEnumMap(t *testing.T) {
	t.Parallel()
	for ud, want := range map[routingv1.Strategy]routingv1.Strategy{
		QUALITY: QUALITY,
		COST:    COST,
		LATENCY: LATENCY,
		UNSPEC:  DEFAULT, // skipped
		DEFAULT: DEFAULT, // defensively skipped
	} {
		if s, _, _, _ := routingclient.ParseStrategy("gpt-4o", http.Header{}, ud); s != want {
			t.Errorf("userDefault=%v → %v, want %v", ud, s, want)
		}
	}
}

// 6.5-UNIT-022 — userDefault=UNSPECIFIED with header/meta present → identical to
// 6.2 (the user-default tier is a pure addition; BR3-3 additive purity).
func TestParseStrategy_UnspecifiedIsAdditivePure(t *testing.T) {
	t.Parallel()
	// header present
	s1, r1, m1, c1 := routingclient.ParseStrategy("gpt-4o", hdr(routingclient.RoutingStrategyHeader, "quality"), UNSPEC)
	if s1 != QUALITY || r1 != "gpt-4o" || m1 || c1 {
		t.Errorf("header+unspec: got (%v,%q,%v,%v), want (QUALITY,gpt-4o,false,false)", s1, r1, m1, c1)
	}
	// meta present
	s2, r2, m2, _ := routingclient.ParseStrategy("he-router-cost", http.Header{}, UNSPEC)
	if s2 != COST || r2 != "" || !m2 {
		t.Errorf("meta+unspec: got (%v,%q,%v), want (COST,\"\",true)", s2, r2, m2)
	}
}

// 6.5-UNIT-029 — Decide resolves userID→userDefault BEFORE ParseStrategy and
// injects the tier (the SelectModel request carries the resolved strategy).
func TestDecide_ResolvesUserDefault(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: ok("doubao-lite", COST, "model_pricing")}
	d := routingclient.NewDecider(fc, nil)
	var gotUID atomic.Value
	d.SetUserDefaultResolver(func(_ context.Context, userID string) routingv1.Strategy {
		gotUID.Store(userID)
		return COST
	})

	if _, err := d.Decide(context.Background(), "gpt-4o", http.Header{}, "user-42", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if fc.gotReq.GetStrategy() != COST {
		t.Errorf("SelectModel strategy = %v, want COST (user-default injected)", fc.gotReq.GetStrategy())
	}
	if fc.gotReq.GetRequestedModel() != "gpt-4o" {
		t.Errorf("requested_model = %q, want gpt-4o (concrete model still sent)", fc.gotReq.GetRequestedModel())
	}
	if gotUID.Load() != "user-42" {
		t.Errorf("resolver got userID %v, want user-42", gotUID.Load())
	}
}

// 6.5-UNIT-022/BR3-3 (Decide) — a nil resolver → byte-for-byte 6.2 (DEFAULT
// passthrough); the user-default tier is off.
func TestDecide_NilResolver_NoRegression(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: ok("gpt-4o", DEFAULT, "default")}
	d := routingclient.NewDecider(fc, nil) // no SetUserDefaultResolver
	if _, err := d.Decide(context.Background(), "gpt-4o", http.Header{}, "u1", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if fc.gotReq.GetStrategy() != DEFAULT {
		t.Errorf("strategy = %v, want DEFAULT (no resolver → 6.2 behaviour)", fc.gotReq.GetStrategy())
	}
}

// 6.5-UNIT-030 — DecideAB does NOT apply the user-default (X-He-AB-Models is the
// directive; the resolver must NOT be consulted on the A/B path).
func TestDecideAB_DoesNotApplyUserDefault(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: &routingv1.SelectModelResponse{
		IsAbTest:         true,
		AbSelectedModels: []string{"gpt-4o", "claude-3"},
	}}
	d := routingclient.NewDecider(fc, nil)
	var resolverCalls atomic.Int64
	d.SetUserDefaultResolver(func(_ context.Context, _ string) routingv1.Strategy {
		resolverCalls.Add(1)
		return COST
	})

	dec, err := d.DecideAB(context.Background(), "gpt-4o", []string{"gpt-4o", "claude-3"}, http.Header{}, "u1", "")
	if err != nil {
		t.Fatalf("DecideAB: %v", err)
	}
	if !dec.IsAbTest {
		t.Errorf("expected A/B decision")
	}
	if resolverCalls.Load() != 0 {
		t.Errorf("user-default resolver consulted %d times on the A/B path; want 0 (Q-G sub-fork)", resolverCalls.Load())
	}
}

// 6.5-UNIT-031 — when the user-default drives the decision, slog carries
// strategy_source=user_default and NEVER user_id (PII discipline).
func TestDecide_UserDefaultSlogSource(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	fc := &fakeClient{resp: ok("doubao-lite", COST, "model_pricing")}
	d := routingclient.NewDecider(fc, logger)
	d.SetUserDefaultResolver(func(_ context.Context, _ string) routingv1.Strategy { return COST })

	if _, err := d.Decide(context.Background(), "gpt-4o", http.Header{}, "secret-user-id", "req_z"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"strategy_source":"user_default"`) {
		t.Errorf("slog missing strategy_source=user_default; got %s", out)
	}
	if strings.Contains(out, "secret-user-id") {
		t.Errorf("slog LEAKED user_id (PII discipline violation); got %s", out)
	}
}

// 6.5 — a valid header outranks the user-default → NO user_default slog source.
func TestDecide_HeaderWins_NoUserDefaultSlog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	fc := &fakeClient{resp: ok("gpt-4o", QUALITY, "model_pricing")}
	d := routingclient.NewDecider(fc, logger)
	d.SetUserDefaultResolver(func(_ context.Context, _ string) routingv1.Strategy { return COST })

	if _, err := d.Decide(context.Background(), "gpt-4o", hdr(routingclient.RoutingStrategyHeader, "quality"), "u1", ""); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if strings.Contains(buf.String(), "user_default") {
		t.Errorf("header should win; no user_default source expected; got %s", buf.String())
	}
}
