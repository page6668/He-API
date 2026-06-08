// Story 6.4 AC1 — Decider.DecideAB (gateway side): populate ab_models on the
// SelectModel request, read back is_ab_test + ab_selected_models, map a leg
// validation failure to a 400 envelope (Q-H), fail-closed on routing-svc
// transport faults (A/B needs the concrete-gate; cannot dispatch unvalidated).
package routingclient_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

func abResp(legs ...string) *routingv1.SelectModelResponse {
	return &routingv1.SelectModelResponse{IsAbTest: true, AbSelectedModels: legs}
}

// DecideAB populates ab_models on the wire request + returns the A/B decision.
func TestDecideAB_PopulatesRequestAndReadsBack(t *testing.T) {
	fc := &fakeClient{resp: abResp("qwen-max", "deepseek-v3")}
	d := routingclient.NewDecider(fc, slog.Default())

	dec, err := d.DecideAB(context.Background(), "qwen-max", []string{"qwen-max", "deepseek-v3"}, http.Header{}, "u1", "req_x")
	if err != nil {
		t.Fatalf("DecideAB err = %v, want nil", err)
	}
	if !dec.IsAbTest {
		t.Error("Decision.IsAbTest = false, want true")
	}
	if len(dec.AbSelectedModels) != 2 || dec.AbSelectedModels[0] != "qwen-max" || dec.AbSelectedModels[1] != "deepseek-v3" {
		t.Errorf("AbSelectedModels = %v, want [qwen-max deepseek-v3]", dec.AbSelectedModels)
	}
	// the gateway populated ab_models on the SelectModel request (BR1-1).
	if got := fc.gotReq.GetAbModels(); len(got) != 2 || got[0] != "qwen-max" || got[1] != "deepseek-v3" {
		t.Errorf("SelectModelRequest.ab_models = %v, want [qwen-max deepseek-v3]", got)
	}
	// Q-E 100ms deadline applied at the call site.
	if !fc.gotDDL {
		t.Error("SelectModel call ctx had no deadline; want the 100ms budget")
	}
}

// a leg validation failure (InvalidArgument from routing-svc) -> 400 envelope.
func TestDecideAB_InvalidArgument_400(t *testing.T) {
	fc := &fakeClient{err: connect.NewError(connect.CodeInvalidArgument, errors.New("bad leg"))}
	d := routingclient.NewDecider(fc, slog.Default())

	_, err := d.DecideAB(context.Background(), "qwen-max", []string{"qwen-max", "he-router-cost"}, http.Header{}, "u1", "")
	var ee *routingclient.EnvelopeError
	if !errors.As(err, &ee) || ee.Code != "400_invalid_request" {
		t.Fatalf("DecideAB err = %v, want EnvelopeError{400_invalid_request}", err)
	}
}

// routing-svc unavailable -> fail-CLOSED 502 (A/B cannot dispatch unvalidated
// legs — contrast the concrete single-model fail-open).
func TestDecideAB_Unavailable_FailsClosed502(t *testing.T) {
	fc := &fakeClient{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	d := routingclient.NewDecider(fc, slog.Default())

	_, err := d.DecideAB(context.Background(), "qwen-max", []string{"qwen-max", "deepseek-v3"}, http.Header{}, "u1", "")
	var ee *routingclient.EnvelopeError
	if !errors.As(err, &ee) || ee.Code != "502_upstream_unavailable" {
		t.Fatalf("DecideAB err = %v, want EnvelopeError{502_upstream_unavailable}", err)
	}
}

// routing disabled (nil client) -> passthrough A/B over the gateway-parsed
// concrete legs (so dispatch tests need no routing-svc).
func TestDecideAB_NilClient_Passthrough(t *testing.T) {
	d := routingclient.NewDecider(nil, slog.Default())
	dec, err := d.DecideAB(context.Background(), "qwen-max", []string{"qwen-max", "deepseek-v3"}, http.Header{}, "u1", "")
	if err != nil {
		t.Fatalf("DecideAB err = %v, want nil", err)
	}
	if !dec.IsAbTest || len(dec.AbSelectedModels) != 2 {
		t.Errorf("passthrough A/B = %+v, want is_ab_test=true + 2 legs", dec)
	}
}
