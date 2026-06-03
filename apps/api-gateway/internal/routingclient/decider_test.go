package routingclient_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/routingclient"
	routingv1 "github.com/he-api/he-api/packages/proto/gen/go/he/routing/v1"
)

// fakeClient is a programmable ClientHandle.
type fakeClient struct {
	resp   *routingv1.SelectModelResponse
	err    error
	gotReq *routingv1.SelectModelRequest
	gotHdr http.Header
	delay  time.Duration
	gotDDL bool // whether the call ctx had a deadline
	calls  int
}

func (f *fakeClient) SelectModel(ctx context.Context, req *routingv1.SelectModelRequest, headers http.Header) (*routingv1.SelectModelResponse, error) {
	f.calls++
	f.gotReq = req
	f.gotHdr = headers
	_, f.gotDDL = ctx.Deadline()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.resp, f.err
}

func hdr(k, v string) http.Header {
	h := http.Header{}
	if v != "" {
		h.Set(k, v)
	}
	return h
}

func ok(model string, strat routingv1.Strategy, src string) *routingv1.SelectModelResponse {
	return &routingv1.SelectModelResponse{SelectedModel: model, StrategyUsed: strat, ScoreSource: src}
}

// 6.2-UNIT-001 — he-router-cost meta-model → COST, requested_model="" (meta wins).
func TestParseStrategy_MetaModelWins(t *testing.T) {
	t.Parallel()
	s, req, isMeta, conflict := routingclient.ParseStrategy("he-router-cost", http.Header{})
	if s != routingv1.Strategy_STRATEGY_COST || req != "" || !isMeta || conflict {
		t.Errorf("got (%v,%q,meta=%v,conflict=%v), want (COST,\"\",true,false)", s, req, isMeta, conflict)
	}
}

// 6.2-UNIT-002 — header strategy + concrete model → QUALITY, requested=concrete.
func TestParseStrategy_Header(t *testing.T) {
	t.Parallel()
	s, req, isMeta, _ := routingclient.ParseStrategy("qwen-max", hdr(routingclient.RoutingStrategyHeader, "quality"))
	if s != routingv1.Strategy_STRATEGY_QUALITY || req != "qwen-max" || isMeta {
		t.Errorf("got (%v,%q,meta=%v), want (QUALITY,qwen-max,false)", s, req, isMeta)
	}
}

// 6.2-UNIT-003 — conflict: meta-model + header → meta wins, conflict=true.
func TestParseStrategy_Conflict(t *testing.T) {
	t.Parallel()
	s, req, isMeta, conflict := routingclient.ParseStrategy("he-router-cost", hdr(routingclient.RoutingStrategyHeader, "quality"))
	if s != routingv1.Strategy_STRATEGY_COST || req != "" || !isMeta || !conflict {
		t.Errorf("got (%v,%q,meta=%v,conflict=%v), want (COST,\"\",true,true)", s, req, isMeta, conflict)
	}
}

// 6.2-UNIT-004 — concrete model, no header → DEFAULT, requested=concrete.
func TestParseStrategy_DefaultPassthrough(t *testing.T) {
	t.Parallel()
	s, req, isMeta, _ := routingclient.ParseStrategy("qwen-max", http.Header{})
	if s != routingv1.Strategy_STRATEGY_DEFAULT || req != "qwen-max" || isMeta {
		t.Errorf("got (%v,%q,meta=%v), want (DEFAULT,qwen-max,false)", s, req, isMeta)
	}
}

// 6.2-UNIT-005 / BLIND-BOUNDARY-005 — unknown header & unknown he-router-xyz →
// default path, no panic.
func TestParseStrategy_UnknownValues(t *testing.T) {
	t.Parallel()
	s, req, _, _ := routingclient.ParseStrategy("qwen-max", hdr(routingclient.RoutingStrategyHeader, "bogus"))
	if s != routingv1.Strategy_STRATEGY_DEFAULT || req != "qwen-max" {
		t.Errorf("unknown header: got (%v,%q), want DEFAULT/qwen-max", s, req)
	}
	s2, req2, isMeta, _ := routingclient.ParseStrategy("he-router-xyz", http.Header{})
	if s2 != routingv1.Strategy_STRATEGY_DEFAULT || req2 != "he-router-xyz" || isMeta {
		t.Errorf("he-router-xyz: got (%v,%q,meta=%v), want DEFAULT/he-router-xyz/false", s2, req2, isMeta)
	}
}

// 6.2-UNIT-006 — quality/latency meta suffixes parse.
func TestParseStrategy_AllMetaSuffixes(t *testing.T) {
	t.Parallel()
	for suffix, want := range map[string]routingv1.Strategy{
		"he-router-quality": routingv1.Strategy_STRATEGY_QUALITY,
		"he-router-latency": routingv1.Strategy_STRATEGY_LATENCY,
		"he-router-cost":    routingv1.Strategy_STRATEGY_COST,
	} {
		if s, _, isMeta, _ := routingclient.ParseStrategy(suffix, http.Header{}); s != want || !isMeta {
			t.Errorf("%s → (%v,meta=%v), want (%v,true)", suffix, s, isMeta, want)
		}
	}
}

// 6.2-UNIT-007 — SelectModel is invoked under a deadline (100ms) at the call site.
func TestDecide_AppliesDeadline(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: ok("qwen-max", routingv1.Strategy_STRATEGY_DEFAULT, "default")}
	d := routingclient.NewDecider(fc, nil)
	if _, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "req_x"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !fc.gotDDL {
		t.Errorf("SelectModel call ctx had no deadline; want 100ms deadline at the call site (Q-E)")
	}
	// he_request_id propagated as a header AND the request field.
	if fc.gotHdr.Get("X-He-Request-Id") != "req_x" {
		t.Errorf("X-He-Request-Id header = %q, want req_x", fc.gotHdr.Get("X-He-Request-Id"))
	}
	if fc.gotReq.GetHeRequestId() != "req_x" {
		t.Errorf("req.he_request_id = %q, want req_x", fc.gotReq.GetHeRequestId())
	}
}

// 6.2-UNIT-010 — score_source comes from resp.ScoreSource (gateway never infers).
func TestDecide_ScoreSourceFromResponse(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: ok("doubao-lite", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	d := routingclient.NewDecider(fc, nil)
	dec, err := d.Decide(context.Background(), "he-router-cost", http.Header{}, "u1", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.SelectedModel != "doubao-lite" || dec.ScoreSource != "model_pricing" {
		t.Errorf("decision = %+v, want doubao-lite/model_pricing", dec)
	}
	// meta-model request → requested_model cleared, ab_models empty (Q-N).
	if fc.gotReq.GetRequestedModel() != "" {
		t.Errorf("requested_model = %q, want empty for meta-model", fc.gotReq.GetRequestedModel())
	}
	if len(fc.gotReq.GetAbModels()) != 0 {
		t.Errorf("ab_models not empty; A/B is Story 6.4 (Q-N)")
	}
}

// Nil client (routing disabled) → passthrough, never an error.
func TestDecide_NilClientPassthrough(t *testing.T) {
	t.Parallel()
	d := routingclient.NewDecider(nil, nil)
	dec, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.SelectedModel != "qwen-max" || !dec.Bypassed {
		t.Errorf("decision = %+v, want qwen-max passthrough bypassed", dec)
	}
}

// 6.2-UNIT-027 — InvalidArgument → 400_invalid_request.
func TestDecide_InvalidArgument(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{err: connect.NewError(connect.CodeInvalidArgument, errors.New("empty"))}
	d := routingclient.NewDecider(fc, nil)
	_, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "")
	assertEnvelope(t, err, "400_invalid_request")
}

// 6.2-UNIT-028 — NotFound + meta-model → 502_upstream_unavailable.
func TestDecide_NotFoundMeta502(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{err: connect.NewError(connect.CodeNotFound, errors.New("no candidates"))}
	d := routingclient.NewDecider(fc, nil)
	_, err := d.Decide(context.Background(), "he-router-cost", http.Header{}, "u1", "")
	assertEnvelope(t, err, "502_upstream_unavailable")
}

// 6.2-UNIT-029 — NotFound + concrete default → 400_invalid_request.
func TestDecide_NotFoundConcrete400(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{err: connect.NewError(connect.CodeNotFound, errors.New("model not found"))}
	d := routingclient.NewDecider(fc, nil)
	_, err := d.Decide(context.Background(), "no-such-model", http.Header{}, "u1", "")
	assertEnvelope(t, err, "400_invalid_request")
}

// 6.2-UNIT-030 — Unavailable + concrete model → fail-OPEN passthrough.
func TestDecide_UnavailableConcreteFailOpen(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	d := routingclient.NewDecider(fc, nil)
	dec, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "")
	if err != nil {
		t.Fatalf("fail-open should not error: %v", err)
	}
	if dec.SelectedModel != "qwen-max" || !dec.Bypassed {
		t.Errorf("decision = %+v, want qwen-max passthrough bypassed", dec)
	}
}

// 6.2-UNIT-031 — Unavailable + meta-model → fail-CLOSED 502.
func TestDecide_UnavailableMetaFailClosed(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{err: connect.NewError(connect.CodeUnavailable, errors.New("down"))}
	d := routingclient.NewDecider(fc, nil)
	_, err := d.Decide(context.Background(), "he-router-quality", http.Header{}, "u1", "")
	assertEnvelope(t, err, "502_upstream_unavailable")
}

// 6.2-BLIND-ERROR-001 — deadline exceeded behaves like Unavailable (Q-G).
func TestDecide_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	// concrete → fail-open
	fc := &fakeClient{err: connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded)}
	d := routingclient.NewDecider(fc, nil)
	dec, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "")
	if err != nil || !dec.Bypassed {
		t.Errorf("concrete deadline → got (%+v,%v), want fail-open bypassed", dec, err)
	}
	// meta → fail-closed
	_, err = d.Decide(context.Background(), "he-router-latency", http.Header{}, "u1", "")
	assertEnvelope(t, err, "502_upstream_unavailable")
}

// 6.2-BLIND-ERROR-002 — blank selected_model → no-route (meta 502 / concrete fail-open).
func TestDecide_BlankSelectedModel(t *testing.T) {
	t.Parallel()
	fc := &fakeClient{resp: ok("", routingv1.Strategy_STRATEGY_COST, "model_pricing")}
	d := routingclient.NewDecider(fc, nil)
	_, err := d.Decide(context.Background(), "he-router-cost", http.Header{}, "u1", "")
	assertEnvelope(t, err, "502_upstream_unavailable")

	dec, err := d.Decide(context.Background(), "qwen-max", http.Header{}, "u1", "")
	if err != nil || !dec.Bypassed || dec.SelectedModel != "qwen-max" {
		t.Errorf("concrete blank → got (%+v,%v), want fail-open qwen-max", dec, err)
	}
}

func assertEnvelope(t *testing.T, err error, wantCode string) {
	t.Helper()
	var ee *routingclient.EnvelopeError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *EnvelopeError(%s)", err, wantCode)
	}
	if ee.Code != wantCode {
		t.Errorf("envelope code = %q, want %q", ee.Code, wantCode)
	}
}
