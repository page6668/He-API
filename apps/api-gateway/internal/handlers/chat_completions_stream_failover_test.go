// Story 6.3 AC3 — gateway failover on the STREAMING path (PRE-FLUSH only).
//
// Scenario trace -> docs/qa/assessments/6.3-test-design-20260603.md:
//
//	6.3-UNIT-035  pre-flush adapter Chat() 502 -> failover to rank-2 -> stream succeeds; header=rank-2
//	6.3-UNIT-036  pre-flush CHUNKER error before first emit -> failover
//	6.3-UNIT-037  pre-flush 504 -> failover to rank-2
//	6.3-UNIT-038  POST-flush upstream error -> inline SSE error frame + [DONE], NO failover (BR3-2)
//	6.3-UNIT-039  pre-flush exhaustion -> BR-2.5 JSON envelope
//	6.3-UNIT-041  TPMDeduct fires EXACTLY once on stream completion after a pre-flush failover (Q-H)
//	6.3-BLIND-RESOURCE-001  the failed pre-flush stream is CLOSED before the next hop re-opens
package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
	adapterv1 "github.com/he-api/he-api/packages/proto/gen/go/he/adapter/v1"
)

// streamingOKHandle returns a handle that streams the canonical 4-chunk happy
// path (flushes, terminal usage present → TPM deduct fires).
func streamingOKHandle() *fakeHandle {
	return &fakeHandle{resp: &fakeStream{chunks: canonicalStreamingChunks()}}
}

// preflushChunkerErrHandle returns a handle whose stream yields NO chunk and an
// error — a pre-flush chunker failure (HeadersFlushed()==false). The created
// stream must be Closed on the way out (BLIND-RESOURCE-001).
func preflushChunkerErrHandle() *fakeHandle {
	return &fakeHandle{resp: &fakeStream{chunks: nil, err: connect.NewError(connect.CodeUnavailable, errFakeUpstream)}}
}

// postflushErrHandle streams one content chunk (flushes headers) THEN errors —
// a post-flush failure (HeadersFlushed()==true → no failover, BR3-2).
func postflushErrHandle() *fakeHandle {
	role := "assistant"
	hi := "Hi"
	chunk := &adapterv1.ChatChunk{
		Id:      "chatcmpl-stream-pf",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "x",
		Choices: []*adapterv1.Choice{{Index: 0, Delta: &adapterv1.Delta{Role: &role, Content: &hi}}},
	}
	return &fakeHandle{resp: &fakeStream{chunks: []*adapterv1.ChatChunk{chunk}, err: connect.NewError(connect.CodeUnavailable, errFakeUpstream)}}
}

const streamBody = `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}],"stream":true}`

// 6.3-UNIT-035 (P0) — pre-flush adapter Chat() 502 → failover to rank-2 → stream
// succeeds; X-He-Selected-Model = rank-2.
func TestFailover_Stream_PreflushChatErr_AdvancesToRank2(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := streamingOKHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, dd)

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want event-stream", ct)
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m2" {
		t.Errorf("X-He-Selected-Model = %q, want m2 (Q-F)", got)
	}
	if !strings.HasSuffix(rr.Body.String(), "data: [DONE]\n\n") {
		t.Errorf("body missing terminal [DONE]; body=%s", rr.Body.String())
	}
	if m1.called != 1 || m2.called != 1 {
		t.Errorf("calls m1=%d m2=%d, want 1/1", m1.called, m2.called)
	}
	// 6.3-UNIT-041 — exactly one TPM deduction on stream completion.
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want 1 (Q-H)", dd.calls)
	}
}

// 6.3-UNIT-036 + BLIND-RESOURCE-001 (P0/P0) — pre-flush CHUNKER error (no emit)
// → failover; the failed stream is Closed before re-opening on the next hop.
func TestFailover_Stream_PreflushChunkerErr_AdvancesAndClosesStream(t *testing.T) {
	m1 := preflushChunkerErrHandle()
	m2 := streamingOKHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "m2" {
		t.Errorf("header = %q, want m2", got)
	}
	// BLIND-RESOURCE-001 — the failed pre-flush stream was Closed.
	if fs, ok := m1.resp.(*fakeStream); ok && !fs.closed {
		t.Errorf("m1 failed stream was not Closed before the next hop (connection leak)")
	}
}

// 6.3-UNIT-037 (P0) — pre-flush 504 timeout → failover to rank-2.
func TestFailover_Stream_Preflush504_Advances(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeDeadlineExceeded}
	m2 := streamingOKHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusOK || rr.Header().Get("X-He-Selected-Model") != "m2" {
		t.Fatalf("status=%d header=%q, want 200 + m2", rr.Code, rr.Header().Get("X-He-Selected-Model"))
	}
}

// 6.3-UNIT-038 (P0) — POST-flush upstream error → inline SSE error frame +
// terminal [DONE], NO failover (BR3-2 — the committed-bytes frontier).
func TestFailover_Stream_PostflushErr_NoFailover(t *testing.T) {
	m1 := postflushErrHandle()
	m2 := streamingOKHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (headers already committed)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q, want event-stream (committed)", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Errorf("post-flush body missing inline error frame + [DONE]; body=%s", body)
	}
	// NO failover — the committed stream owns the error; m2 is never tried.
	if m2.called != 0 {
		t.Errorf("m2 called = %d, want 0 (NO post-flush failover, BR3-2)", m2.called)
	}
}

// 6.3-UNIT-040 (P1) — a non-retriable pre-flush error is TERMINAL on the stream
// path too: the BR-2.5 JSON envelope is written with the 4xx code and NO
// failover (invariant parity with AC2 / Q-C).
func TestFailover_Stream_PreflushNonRetriable_NoFailover(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeInvalidArgument}
	m2 := streamingOKHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (non-retriable pre-flush terminal)", rr.Code)
	}
	if m2.called != 0 {
		t.Errorf("m2 called = %d, want 0 (NO failover on non-retriable)", m2.called)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// 6.3-UNIT-039 (P0) — pre-flush exhaustion (all hops fail pre-flush) → the
// BR-2.5 JSON envelope (status not yet committed).
func TestFailover_Stream_PreflushExhaustion_JSONEnvelope(t *testing.T) {
	m1 := &failingHandle{code: connect.CodeUnavailable}
	m2 := &failingHandle{code: connect.CodeUnavailable}
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"m1": m1, "m2": m2})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("m1", "m2")}, reg, dd)

	rr := doRoutedRequest(t, h, streamBody, "")
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (pre-flush JSON envelope)", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (pre-flush envelope)", ct)
	}
	if m1.called != 1 || m2.called != 1 {
		t.Errorf("calls m1=%d m2=%d, want 1/1", m1.called, m2.called)
	}
	if dd.calls != 0 {
		t.Errorf("TPMDeduct calls = %d, want 0 (no committed stream)", dd.calls)
	}
}
