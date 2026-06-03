// Package tests carries routing-svc's over-the-wire Connect-RPC integration
// scenarios (Story 6.1). These exercise real serialization + transport through
// the assembled server.Handler (the same wiring cmd/server boots), using an
// httptest server + the generated Connect client — no testcontainers are
// needed because routing-svc is stateless in 6.1 (no external deps, BR1-1).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-INT-001  server boots w/ valid catalogue -> RoutingService served
//	6.1-INT-010  SelectModel over the wire — all 4 slugs return success + shape
//	6.1-INT-011  SelectModel over the wire — 3 error paths -> correct gRPC codes
//	6.1-BLIND-CONCURRENCY-002  concurrent SelectModel RPCs, read-only snapshot, no race (-race)
package tests

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/routing-svc/internal/server"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1/routingv1connect"
)

// newWireClient assembles the production server.Handler, serves it on an
// httptest server, and returns a Connect client bound to it.
func newWireClient(t *testing.T) (routingv1connect.RoutingServiceClient, func()) {
	t.Helper()
	srv, err := server.New(server.Options{
		Catalogue:  modelscatalogue.DefaultCatalogue,
		Strategies: strategy.DefaultStrategies(strategy.Deps{}),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler)
	client := routingv1connect.NewRoutingServiceClient(http.DefaultClient, ts.URL)
	return client, ts.Close
}

// firstAlphabeticalDefault is the smallest model id in the shared catalogue —
// the result every named stub returns (Q-G). Computed from the live registry
// so the assertion tracks the seed.
func firstAlphabeticalDefault(t *testing.T) string {
	t.Helper()
	best := ""
	for _, e := range modelscatalogue.DefaultCatalogue.List() {
		if best == "" || e.ID < best {
			best = e.ID
		}
	}
	if best == "" {
		t.Fatal("empty default catalogue")
	}
	return best
}

// 6.1-INT-001 + 6.1-INT-010 — server serves SelectModel; all 4 slugs succeed
// with the correct response shape over the wire.
func Test_INT_010_four_slugs_over_the_wire(t *testing.T) {
	client, closeFn := newWireClient(t)
	defer closeFn()

	wantNamed := firstAlphabeticalDefault(t)

	cases := []struct {
		strategy     routingv1.Strategy
		requested    string
		wantSelected string
		wantUsed     routingv1.Strategy
	}{
		{routingv1.Strategy_STRATEGY_QUALITY, "", wantNamed, routingv1.Strategy_STRATEGY_QUALITY},
		{routingv1.Strategy_STRATEGY_COST, "", wantNamed, routingv1.Strategy_STRATEGY_COST},
		{routingv1.Strategy_STRATEGY_LATENCY, "", wantNamed, routingv1.Strategy_STRATEGY_LATENCY},
		{routingv1.Strategy_STRATEGY_DEFAULT, "deepseek-v3", "deepseek-v3", routingv1.Strategy_STRATEGY_DEFAULT},
	}
	for _, tc := range cases {
		resp, err := client.SelectModel(context.Background(), connect.NewRequest(&routingv1.SelectModelRequest{
			Strategy:       tc.strategy,
			RequestedModel: tc.requested,
			HeRequestId:    "int-010",
		}))
		if err != nil {
			t.Fatalf("slug %v: SelectModel err = %v", tc.strategy, err)
		}
		msg := resp.Msg
		if msg.GetSelectedModel() != tc.wantSelected {
			t.Errorf("slug %v: selected_model = %q, want %q", tc.strategy, msg.GetSelectedModel(), tc.wantSelected)
		}
		if msg.GetStrategyUsed() != tc.wantUsed {
			t.Errorf("slug %v: strategy_used = %v, want %v", tc.strategy, msg.GetStrategyUsed(), tc.wantUsed)
		}
		if msg.GetAdapterEndpoint() != "" {
			t.Errorf("slug %v: adapter_endpoint = %q, want EMPTY (Q-K)", tc.strategy, msg.GetAdapterEndpoint())
		}
		if msg.GetIsAbTest() || len(msg.GetAbSelectedModels()) != 0 {
			t.Errorf("slug %v: A/B fields populated, want empty", tc.strategy)
		}
	}
}

// 6.1-INT-011 — the 3 error paths map to the correct gRPC codes end-to-end.
func Test_INT_011_error_paths_over_the_wire(t *testing.T) {
	client, closeFn := newWireClient(t)
	defer closeFn()

	cases := []struct {
		name string
		req  *routingv1.SelectModelRequest
		want connect.Code
	}{
		{"empty requested_model (default)", &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_DEFAULT}, connect.CodeInvalidArgument},
		{"requested_model not in catalogue", &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_DEFAULT, RequestedModel: "ghost"}, connect.CodeNotFound},
		{"unknown strategy enum", &routingv1.SelectModelRequest{Strategy: routingv1.Strategy(99)}, connect.CodeInvalidArgument},
	}
	for _, tc := range cases {
		_, err := client.SelectModel(context.Background(), connect.NewRequest(tc.req))
		if connect.CodeOf(err) != tc.want {
			t.Errorf("%s: code = %v, want %v (err=%v)", tc.name, connect.CodeOf(err), tc.want, err)
		}
	}
}

// 6.1-BLIND-CONCURRENCY-002 — concurrent SelectModel RPCs read the boot-loaded
// snapshot read-only; no data race / torn read. Run with `go test -race`.
func Test_BLIND_CONCURRENCY_002_concurrent_rpcs(t *testing.T) {
	client, closeFn := newWireClient(t)
	defer closeFn()

	want := firstAlphabeticalDefault(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	got := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			resp, err := client.SelectModel(context.Background(), connect.NewRequest(&routingv1.SelectModelRequest{
				Strategy: routingv1.Strategy_STRATEGY_QUALITY,
			}))
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = resp.Msg.GetSelectedModel()
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("rpc[%d] err = %v", i, errs[i])
		}
		if got[i] != want {
			t.Errorf("rpc[%d] selected = %q, want %q (torn read?)", i, got[i], want)
		}
	}
}
