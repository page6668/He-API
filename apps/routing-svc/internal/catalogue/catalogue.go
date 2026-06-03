// Package catalogue is routing-svc's boot-time catalogue loader. In Story 6.1
// it returns the static shared snapshot (single source of truth, Q-A); Story
// 6.2 swaps in a PG-backed (he_api.models) read-through loader behind this same
// seam — callers depend on the returned Catalogue, not on how it was loaded.
package catalogue

import (
	modelscatalogue "github.com/he-api/he-api/packages/models-catalogue"
)

// Load returns the catalogue snapshot routing-svc serves from. It is the
// shared DefaultCatalogue (the same registry the api-gateway consumes), so the
// two services never drift (Story 6.1 BLIND-DATA-002). Rebuilt on pod restart;
// runtime mutation / hot-reload is out of scope in 6.1.
func Load() modelscatalogue.Catalogue {
	return modelscatalogue.DefaultCatalogue
}
