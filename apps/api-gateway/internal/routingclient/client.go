// Package routingclient is the gateway-side client for the Epic-6
// RoutingService.SelectModel RPC (Story 6.2 — the FIRST gateway→routing-svc
// caller; routing-svc shipped the server in 6.1). It mirrors the
// adapterclient shape (Architect Round-2 OQ4 cascade): a ClientHandle
// abstraction over a Connect-RPC client, an env-var startup loader, and — new
// to this package — a Decider that runs the full hot-path decision (strategy
// resolution Q-I → 100ms-deadline SelectModel Q-E → gRPC→§5.1.2 envelope
// mapping Q-H → fail-open/closed Q-G → observability Q-M).
//
// When ROUTING_SVC_ENDPOINT is unset, LoadFromEnv returns nil and the Decider
// degrades to req.Model passthrough — the gateway keeps working exactly as it
// did pre-6.2 (zero-config back-compat for environments that have not yet wired
// routing-svc).
package routingclient

import (
	"context"
	"net/http"
	"os"

	"connectrpc.com/connect"

	obs "github.com/he-api/he-api/packages/go-observability"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
	"github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1/routingv1connect"
)

// EndpointEnv is the env var the startup loader reads for routing-svc's
// Connect-RPC endpoint. Empty → routing disabled (passthrough).
const EndpointEnv = "ROUTING_SVC_ENDPOINT"

// ClientHandle is the abstraction the Decider invokes. The concrete type wraps
// a Connect-RPC RoutingServiceClient; tests inject a fake to drive the decision
// logic without a transport.
type ClientHandle interface {
	SelectModel(ctx context.Context, req *routingv1.SelectModelRequest, headers http.Header) (*routingv1.SelectModelResponse, error)
}

// connectClientHandle is the production ClientHandle backed by a Connect-RPC
// client (mirrors adapterclient.connectClientHandle).
type connectClientHandle struct {
	httpc  *http.Client
	client routingv1connect.RoutingServiceClient
}

func newConnectClientHandle(endpoint string) *connectClientHandle {
	httpc := obs.NewHTTPClient() // Story 9.4 BR-TR-2 — traceparent-injecting transport
	return &connectClientHandle{
		httpc:  httpc,
		client: routingv1connect.NewRoutingServiceClient(httpc, endpoint),
	}
}

func (c *connectClientHandle) SelectModel(ctx context.Context, req *routingv1.SelectModelRequest, headers http.Header) (*routingv1.SelectModelResponse, error) {
	creq := connect.NewRequest(req)
	for k, vs := range headers {
		for _, v := range vs {
			creq.Header().Add(k, v)
		}
	}
	resp, err := c.client.SelectModel(ctx, creq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// LoadFromEnv returns a ClientHandle when ROUTING_SVC_ENDPOINT is set, else nil
// (routing disabled → the Decider passes req.Model through, preserving today's
// behaviour).
func LoadFromEnv() ClientHandle {
	endpoint := os.Getenv(EndpointEnv)
	if endpoint == "" {
		return nil
	}
	return newConnectClientHandle(endpoint)
}
