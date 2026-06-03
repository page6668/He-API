// Tests for the lifted models-catalogue package (Story 6.1 AC4, Q-A option (a)).
//
// Scenario trace -> docs/qa/assessments/6.1-test-design-20260603.md:
//
//	6.1-UNIT-040  List() returns the seed model-id rows verbatim
//	6.1-UNIT-041  Find() returns the capability fields verbatim per model
//	6.1-UNIT-042  model without a capability entry -> panic at construction (4.7 BR-1.3)
//	6.1-BLIND-DATA-001  reverse 1:1 — capability orphan also panics (no orphan either direction)
//	6.1-BLIND-ERROR-002 malformed entry (missing ID) -> panic (no silent half-load)
//
// Note: the design says "10 seed rows" but the authoritative Story-4.7
// catalogue (Architect OQ1) is 11 rows — the binding constraint is verbatim
// parity with the gateway source + 4.7-INT-001 byte-identity, asserted in
// apps/api-gateway/internal/handlers (INT-020), not a hardcoded count here.
package modelscatalogue

import (
	"reflect"
	"sync"
	"testing"
)

// 6.1-UNIT-040 (P0) — List() returns the seed model-id rows verbatim, in
// declaration order (BR-1.10 load-bearing ordering).
func Test_UNIT_040_List_returns_seed_rows_verbatim(t *testing.T) {
	c := NewFromRegistry(DefaultRegistry)
	got := c.List()

	if len(got) != len(DefaultRegistry.Models) {
		t.Fatalf("List() len = %d, want %d", len(got), len(DefaultRegistry.Models))
	}
	for i, seed := range DefaultRegistry.Models {
		if got[i].ID != seed.ID {
			t.Errorf("List()[%d].ID = %q, want %q (declaration order)", i, got[i].ID, seed.ID)
		}
		if got[i].Vendor != seed.Vendor {
			t.Errorf("List()[%d].Vendor = %q, want %q", i, got[i].Vendor, seed.Vendor)
		}
	}
}

// 6.1-UNIT-041 (P0) — Find() returns the capability fields verbatim per model.
func Test_UNIT_041_Find_returns_capabilities_verbatim(t *testing.T) {
	c := NewFromRegistry(DefaultRegistry)
	for id, want := range DefaultRegistry.Capabilities {
		entry, ok := c.Find(id)
		if !ok {
			t.Fatalf("Find(%q) not found", id)
		}
		if !reflect.DeepEqual(entry.Capabilities, want) {
			t.Errorf("Find(%q).Capabilities = %+v, want %+v", id, entry.Capabilities, want)
		}
	}
	if _, ok := c.Find("does-not-exist"); ok {
		t.Errorf("Find(does-not-exist) returned ok=true")
	}
}

// 6.1-UNIT-042 (P0) — a model without a capability entry panics at
// construction (preserves Story-4.7 BR-1.3 1:1 invariant inside the package).
func Test_UNIT_042_model_without_capability_panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewFromRegistry did not panic on a model missing its capability entry")
		}
	}()
	NewFromRegistry(Registry{
		Models:       []ModelSeed{{ID: "orphan-model", Vendor: "x"}},
		Capabilities: map[string]Capabilities{}, // no row for orphan-model
	})
}

// 6.1-BLIND-DATA-001 (P0) — reverse 1:1: a capability entry with no matching
// model also panics (no orphan in either direction).
func Test_BLIND_DATA_001_capability_orphan_panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewFromRegistry did not panic on a capability entry with no matching model")
		}
	}()
	NewFromRegistry(Registry{
		Models: []ModelSeed{{ID: "m1", Vendor: "x"}},
		Capabilities: map[string]Capabilities{
			"m1":                {Chat: true},
			"orphan-capability": {Chat: true}, // no matching model
		},
	})
}

// 6.1-BLIND-ERROR-002 (P2) — a malformed seed (empty ID) panics rather than
// loading a silent half-catalogue.
func Test_BLIND_ERROR_002_empty_id_panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("NewFromRegistry did not panic on a model seed with empty ID")
		}
	}()
	NewFromRegistry(Registry{
		Models:       []ModelSeed{{ID: "", Vendor: "x"}},
		Capabilities: map[string]Capabilities{"": {Chat: true}},
	})
}

// An empty registry is valid (NOT a panic) — it yields an empty catalogue;
// rejecting empty is the consumer's boot-time job (routing-svc NewEngine).
func Test_empty_registry_is_valid_empty_catalogue(t *testing.T) {
	c := NewFromRegistry(Registry{})
	if c.Len() != 0 {
		t.Fatalf("empty registry -> Len() = %d, want 0", c.Len())
	}
	if got := c.List(); len(got) != 0 {
		t.Fatalf("empty registry -> List() len = %d, want 0", len(got))
	}
}

// List() returns a defensive copy — mutating the result must not affect the
// shared catalogue or other readers.
func Test_List_returns_defensive_copy(t *testing.T) {
	c := NewFromRegistry(DefaultRegistry)
	first := c.List()
	if len(first) == 0 {
		t.Fatal("empty default catalogue")
	}
	first[0].ID = "MUTATED"
	second := c.List()
	if second[0].ID == "MUTATED" {
		t.Errorf("List() leaked shared backing array — mutation visible across calls")
	}
}

// 6.1-BLIND-CONCURRENCY-001 (P1, engine-level companion at the package level):
// N concurrent List()/Find() reads on the shared catalogue are race-free and
// deterministic. Run with `go test -race`.
func Test_BLIND_CONCURRENCY_001_concurrent_reads_race_free(t *testing.T) {
	c := NewFromRegistry(DefaultRegistry)
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = c.List()
			_, _ = c.Find("qwen-max")
			_ = c.Len()
		}()
	}
	wg.Wait()
}
