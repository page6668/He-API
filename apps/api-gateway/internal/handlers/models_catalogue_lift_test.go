// Story 6.1 AC4 — gateway-side guard that the catalogue lift to the shared
// packages/models-catalogue module preserves the gateway's contract AND that
// the gateway + routing-svc read the IDENTICAL snapshot from the single SoT.
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-BLIND-DATA-002  api-gateway and routing-svc read the identical snapshot
//	                    from the single SoT (no cross-consumer drift). The
//	                    routing-svc side uses modelscatalogue.DefaultCatalogue
//	                    directly (apps/routing-svc/internal/catalogue.Load());
//	                    this test proves the gateway's reconstructed vars are
//	                    derived from — and stay identical to — that same SoT.
package handlers

import (
	"testing"

	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

// 6.1-BLIND-DATA-002 — the gateway's modelsCatalogue / capabilitiesByModelID
// are reconstructed from modelscatalogue.DefaultCatalogue, so they match it
// row-for-row (same ids, same order, same vendor->owned_by, same capabilities).
// routing-svc consumes the same DefaultCatalogue, so the two services cannot
// drift.
func Test_BLIND_DATA_002_gateway_matches_shared_SoT(t *testing.T) {
	shared := modelscatalogue.DefaultCatalogue.List()

	if len(modelsCatalogue) != len(shared) {
		t.Fatalf("gateway catalogue len = %d, shared SoT len = %d (drift)", len(modelsCatalogue), len(shared))
	}
	for i, e := range shared {
		if modelsCatalogue[i].ID != e.ID {
			t.Errorf("row %d: gateway id %q != shared id %q (order/identity drift)", i, modelsCatalogue[i].ID, e.ID)
		}
		if modelsCatalogue[i].OwnedBy != e.Vendor {
			t.Errorf("row %d (%s): gateway owned_by %q != shared vendor %q", i, e.ID, modelsCatalogue[i].OwnedBy, e.Vendor)
		}
		if modelsCatalogue[i].Object != "model" {
			t.Errorf("row %d (%s): gateway object = %q, want \"model\"", i, e.ID, modelsCatalogue[i].Object)
		}
		caps, ok := capabilitiesByModelID[e.ID]
		if !ok {
			t.Errorf("row %d (%s): gateway has no capability entry", i, e.ID)
			continue
		}
		c := e.Capabilities
		if caps.Chat != c.Chat || caps.Streaming != c.Streaming || caps.FunctionCalling != c.FunctionCalling ||
			caps.Vision != c.Vision || caps.JSONMode != c.JSONMode ||
			caps.ContextWindowTokens != c.ContextWindowTokens || caps.MaxOutputTokens != c.MaxOutputTokens {
			t.Errorf("row %d (%s): gateway capabilities %+v != shared %+v", i, e.ID, caps, c)
		}
	}
}
