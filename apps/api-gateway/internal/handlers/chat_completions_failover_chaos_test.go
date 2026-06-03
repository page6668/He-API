//go:build chaos

// Story 6.3 AC4 — `[CHAOS]` upstream-fault injection (testing-strategy.md
// §混沌测试 "上游 502 时 failover 正确切换").
//
// Build tag `chaos` — NOT run in default CI; gated nightly suite per the Story
// 4.x / 5.1 / 5.3 adapter-chaos precedent (`go test -tags chaos ./...`).
//
// The test design names Toxiproxy (via testcontainers) for the network
// primitive. This implementation injects the SAME upstream faults (a 502
// connection-refusal and a connection-timeout → 504) at the adapter Connect-RPC
// boundary via a fault handle — the **invariant under test** is identical:
//
//	6.3-INT-007  upstream 502 on rank-1 → request SUCCEEDS on rank-2;
//	             X-He-Selected-Model = rank-2; exactly-one TPMDeduct (BR4-4)
//	6.3-INT-008  connection-timeout (→504) on rank-1 → failover to rank-2
//
// The Toxiproxy lane (real RST / latency toxics over a docker upstream) is the
// environmental superset run in the nightly chaos job; this lane proves the
// gateway-side switch logic without a docker dependency (mirrors the Story-5.3
// miniredis-vs-Toxiproxy substitution).
package handlers_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/he-api/he-api/apps/api-gateway/internal/adapterclient"
)

// 6.3-INT-007 [CHAOS] — upstream 502 on rank-1 → rank-2 serves; one deduction.
func TestChaos_Failover_Upstream502_SwitchesToRank2(t *testing.T) {
	rank1 := &failingHandle{code: connect.CodeUnavailable} // 502_upstream_unavailable
	rank2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"rank1": rank1, "rank2": rank2})
	dd := &countingDeducter{}
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("rank1", "rank2")}, reg, dd)

	rr := doRoutedRequest(t, h, `{"model":"he-router-cost","messages":[{"role":"user","content":"hi"}]}`, "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-He-Selected-Model"); got != "rank2" {
		t.Errorf("X-He-Selected-Model = %q, want rank2 (failover switched)", got)
	}
	if dd.calls != 1 {
		t.Errorf("TPMDeduct calls = %d, want exactly 1 (BR4-4 / Q-H)", dd.calls)
	}
}

// 6.3-INT-008 [CHAOS] — connection-timeout (→504) on rank-1 → failover to rank-2.
func TestChaos_Failover_UpstreamTimeout_SwitchesToRank2(t *testing.T) {
	rank1 := &failingHandle{code: connect.CodeDeadlineExceeded} // 504_upstream_timeout
	rank2 := okHandle()
	reg := adapterclient.NewRegistryFromHandles(map[string]adapterclient.ClientHandle{"rank1": rank1, "rank2": rank2})
	h := failoverHandler(t, &fakeRoutingClient{resp: routedRespChain("rank1", "rank2")}, reg, &countingDeducter{})

	rr := doRoutedRequest(t, h, `{"model":"he-router-latency","messages":[{"role":"user","content":"hi"}]}`, "")

	if rr.Code != http.StatusOK || rr.Header().Get("X-He-Selected-Model") != "rank2" {
		t.Fatalf("status=%d header=%q, want 200 + rank2", rr.Code, rr.Header().Get("X-He-Selected-Model"))
	}
}
