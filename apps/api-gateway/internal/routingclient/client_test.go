package routingclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1/routingv1connect"
)

// stubHandler is an in-test RoutingService server: it echoes the requested
// strategy and reflects the he_request_id header so the client wrapper's header
// propagation + response decoding are exercised over a real transport.
type stubHandler struct {
	gotHeader string
}

func (s *stubHandler) SelectModel(ctx context.Context, req *connect.Request[routingv1.SelectModelRequest]) (*connect.Response[routingv1.SelectModelResponse], error) {
	s.gotHeader = req.Header().Get("X-He-Request-Id")
	return connect.NewResponse(&routingv1.SelectModelResponse{
		SelectedModel: "doubao-lite",
		StrategyUsed:  req.Msg.GetStrategy(),
		ScoreSource:   "model_pricing",
	}), nil
}

// LoadFromEnv returns nil when the endpoint env is unset (routing disabled).
func TestLoadFromEnv_UnsetReturnsNil(t *testing.T) {
	t.Setenv(routingclient.EndpointEnv, "")
	if c := routingclient.LoadFromEnv(); c != nil {
		t.Errorf("LoadFromEnv() = %v, want nil when %s unset", c, routingclient.EndpointEnv)
	}
}

// 6.2-RESOURCE-002 (transport) + wire round-trip: LoadFromEnv builds a working
// client; the Decider drives a real Connect round-trip end to end.
func TestLoadFromEnv_WireRoundTrip(t *testing.T) {
	stub := &stubHandler{}
	mux := http.NewServeMux()
	path, h := routingv1connect.NewRoutingServiceHandler(stub)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv(routingclient.EndpointEnv, srv.URL)
	client := routingclient.LoadFromEnv()
	if client == nil {
		t.Fatal("LoadFromEnv() = nil, want a client when endpoint set")
	}

	d := routingclient.NewDecider(client, nil)
	dec, err := d.Decide(context.Background(), "he-router-cost", http.Header{}, "u1", "req_wire")
	if err != nil {
		t.Fatalf("Decide over wire: %v", err)
	}
	if dec.SelectedModel != "doubao-lite" || dec.ScoreSource != "model_pricing" {
		t.Errorf("decision = %+v, want doubao-lite/model_pricing", dec)
	}
	if dec.Strategy != routingv1.Strategy_STRATEGY_COST {
		t.Errorf("strategy = %v, want COST (echoed)", dec.Strategy)
	}
	if stub.gotHeader != "req_wire" {
		t.Errorf("server saw X-He-Request-Id = %q, want req_wire", stub.gotHeader)
	}
}
