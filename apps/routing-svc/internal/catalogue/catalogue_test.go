// Story 6.1 UNIT-043 — routing-svc boot-loads its catalogue from the shared
// single source of truth (packages/models-catalogue), with no duplication.
package catalogue

import (
	"testing"

	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

// 6.1-UNIT-043 — Load returns the shared DefaultCatalogue (single SoT, BR4-1):
// non-empty and identical to the package the api-gateway also consumes.
func Test_UNIT_043_Load_returns_shared_SoT(t *testing.T) {
	got := Load()
	if got.Len() == 0 {
		t.Fatal("Load() returned an empty catalogue")
	}
	want := modelscatalogue.DefaultCatalogue
	if got.Len() != want.Len() {
		t.Fatalf("Load() len = %d, shared DefaultCatalogue len = %d (not the single SoT)", got.Len(), want.Len())
	}
	for _, e := range want.List() {
		ge, ok := got.Find(e.ID)
		if !ok {
			t.Errorf("Load() missing model %q present in the shared SoT", e.ID)
			continue
		}
		if ge.Vendor != e.Vendor {
			t.Errorf("model %q: Load vendor %q != shared %q", e.ID, ge.Vendor, e.Vendor)
		}
	}
}
