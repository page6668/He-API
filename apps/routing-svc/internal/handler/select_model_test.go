// SelectModel handler tests (Story 6.1 AC3 + boundary overlays).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-030  valid req -> {selected_model, strategy_used, is_ab_test=false, ab_selected_models=[]}, adapter_endpoint EMPTY (Q-K)
//	6.1-UNIT-031  empty requested_model (default/unspecified) -> InvalidArgument (Q-F)
//	6.1-UNIT-032  requested_model not in catalogue (default) -> NotFound (Q-F)
//	6.1-UNIT-033  ErrUnknownStrategy -> InvalidArgument (Q-F)
//	6.1-UNIT-034  he_request_id echoed to slog ONLY (Q-H)
//	6.1-UNIT-035  response NEVER populates adapter_endpoint (Q-K)
//	6.1-UNIT-036  strategy_used echoes actually-fired strategy — UNSPECIFIED -> DEFAULT (Q-I/Q-D)
//	6.1-BLIND-BOUNDARY-001  whitespace-only requested_model -> InvalidArgument
//	6.1-BLIND-BOUNDARY-003  ab_models populated -> ignored; is_ab_test=false, ab_selected_models=[]
//	6.1-BLIND-BOUNDARY-004  empty he_request_id -> no validation error
package handler

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/routing-svc/internal/engine"
	"github.com/he-api/he-api/apps/routing-svc/internal/strategy"
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

func newTestServer(t *testing.T, logger *slog.Logger, ids ...string) *RoutingServer {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{"alpha", "beta", "gamma"}
	}
	reg := modelscatalogue.Registry{Capabilities: map[string]modelscatalogue.Capabilities{}}
	for _, id := range ids {
		reg.Models = append(reg.Models, modelscatalogue.ModelSeed{ID: id, Vendor: "test"})
		reg.Capabilities[id] = modelscatalogue.Capabilities{Chat: true}
	}
	e, err := engine.NewEngine(modelscatalogue.NewFromRegistry(reg), strategy.DefaultStrategies(strategy.Deps{}))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return NewRoutingServer(e, logger)
}

func call(s *RoutingServer, msg *routingv1.SelectModelRequest) (*routingv1.SelectModelResponse, error) {
	resp, err := s.SelectModel(context.Background(), connect.NewRequest(msg))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// 6.1-UNIT-030 + 6.1-UNIT-035 (P0/P1)
func Test_UNIT_030_valid_request_shape(t *testing.T) {
	s := newTestServer(t, slog.Default())
	resp, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_QUALITY})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	if resp.GetSelectedModel() != "alpha" { // first-alphabetical of alpha/beta/gamma
		t.Errorf("selected_model = %q, want alpha", resp.GetSelectedModel())
	}
	if resp.GetStrategyUsed() != routingv1.Strategy_STRATEGY_QUALITY {
		t.Errorf("strategy_used = %v, want QUALITY", resp.GetStrategyUsed())
	}
	if resp.GetIsAbTest() {
		t.Error("is_ab_test = true, want false")
	}
	if len(resp.GetAbSelectedModels()) != 0 {
		t.Errorf("ab_selected_models = %v, want empty", resp.GetAbSelectedModels())
	}
	if resp.GetAdapterEndpoint() != "" {
		t.Errorf("adapter_endpoint = %q, want EMPTY (Q-K reserved)", resp.GetAdapterEndpoint())
	}
}

// 6.1-UNIT-031 + 6.1-BLIND-BOUNDARY-001 (P0/P2)
func Test_UNIT_031_empty_and_whitespace_requested_model_InvalidArgument(t *testing.T) {
	s := newTestServer(t, slog.Default())
	for _, rm := range []string{"", "   ", "\t\n"} {
		_, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_DEFAULT, RequestedModel: rm})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("requested_model=%q -> code %v, want InvalidArgument", rm, connect.CodeOf(err))
		}
	}
	// also the UNSPECIFIED zero-value path requires it.
	_, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_UNSPECIFIED})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("UNSPECIFIED + empty requested_model -> code %v, want InvalidArgument", connect.CodeOf(err))
	}
}

// 6.1-UNIT-032 (P0)
func Test_UNIT_032_requested_model_not_in_catalogue_NotFound(t *testing.T) {
	s := newTestServer(t, slog.Default())
	_, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_DEFAULT, RequestedModel: "ghost-model"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (ErrNoCandidates->NotFound, Q-F)", connect.CodeOf(err))
	}
}

// 6.1-UNIT-033 + 6.1-BLIND-BOUNDARY-002 (P0/P1)
func Test_UNIT_033_unknown_strategy_InvalidArgument(t *testing.T) {
	s := newTestServer(t, slog.Default())
	for _, slug := range []routingv1.Strategy{routingv1.Strategy(5), routingv1.Strategy(99)} {
		_, err := call(s, &routingv1.SelectModelRequest{Strategy: slug})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("strategy=%d -> code %v, want InvalidArgument (ErrUnknownStrategy->InvalidArgument)", slug, connect.CodeOf(err))
		}
	}
}

// 6.1-UNIT-034 + 6.1-BLIND-BOUNDARY-004 (P1/P2)
func Test_UNIT_034_he_request_id_slog_only(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	s := newTestServer(t, logger)

	const reqID = "he-req-abc-123"
	resp, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_QUALITY, HeRequestId: reqID})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, reqID) {
		t.Errorf("he_request_id %q not echoed to slog; log = %s", reqID, logged)
	}
	if strings.Contains(logged, "user_id") {
		t.Errorf("slog leaked user_id (PII discipline); log = %s", logged)
	}
	// never returned in the response (no field carries it).
	if strings.Contains(resp.String(), reqID) {
		t.Errorf("he_request_id leaked into response: %s", resp.String())
	}

	// BLIND-BOUNDARY-004: empty he_request_id -> no error.
	if _, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_COST}); err != nil {
		t.Errorf("empty he_request_id errored: %v", err)
	}
}

// 6.1-UNIT-036 (P1)
func Test_UNIT_036_strategy_used_echoes_default_for_unspecified(t *testing.T) {
	s := newTestServer(t, slog.Default())
	resp, err := call(s, &routingv1.SelectModelRequest{Strategy: routingv1.Strategy_STRATEGY_UNSPECIFIED, RequestedModel: "beta"})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	if resp.GetStrategyUsed() != routingv1.Strategy_STRATEGY_DEFAULT {
		t.Errorf("strategy_used = %v, want DEFAULT (Q-I/Q-D)", resp.GetStrategyUsed())
	}
	if resp.GetSelectedModel() != "beta" {
		t.Errorf("selected_model = %q, want beta (verbatim)", resp.GetSelectedModel())
	}
}

// 6.1-BLIND-BOUNDARY-003 (P2)
func Test_BLIND_BOUNDARY_003_ab_models_ignored(t *testing.T) {
	s := newTestServer(t, slog.Default())
	resp, err := call(s, &routingv1.SelectModelRequest{
		Strategy: routingv1.Strategy_STRATEGY_QUALITY,
		AbModels: []string{"alpha", "beta"}, // populated but ignored in 6.1
	})
	if err != nil {
		t.Fatalf("SelectModel err = %v", err)
	}
	if resp.GetIsAbTest() {
		t.Error("is_ab_test = true, want false (A/B is Story 6.4)")
	}
	if len(resp.GetAbSelectedModels()) != 0 {
		t.Errorf("ab_selected_models = %v, want empty", resp.GetAbSelectedModels())
	}
}
