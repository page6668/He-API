// Package upstream owns the Doubao (Volcengine Ark) HTTPS-API wire
// types + the HTTP/2-preferred client + the NEW-for-Story-4.5
// endpoint-id static lookup map used by translate.go to rewrite the
// outbound `model` body field from the consumer-facing friendly id
// (`doubao-pro` / `doubao-lite`) to the opaque Volcengine endpoint id
// (`ep-20240xxx-xxxx`-shaped).
//
// Architect Round 1 OQ-4.5-3 ratification: two-entry static lookup map
// keyed off env vars DOUBAO_PRO_ENDPOINT_ID + DOUBAO_LITE_ENDPOINT_ID.
// Per OQ-4.5-4, env vars are sourced from a ConfigMap (`doubao-endpoint-ids`)
// mounted via `envFrom: configMapRef` on the adapter Deployment.
//
// Architect Round 1 m-1 refactor: package-level `var x = map{... os.Getenv}`
// evaluates at process-import time, which breaks `t.Setenv` based tests
// (the map snapshot is taken once and never re-read). Story 4.5 uses an
// explicit constructor `New(envProvider)` so tests can inject an env
// with the variable set / unset / empty without process restart.
//
// Architect Round 1 l-1 ratification: NO ReverseLookup — the back-translate
// path uses `req.Model` from the Service per-request closure (rotation-safe;
// see Story 4.5 Architect Review Results m-2).
package upstream

import (
	"errors"
	"os"
)

// ErrUnsupportedModel is returned by Lookup when the friendly model id is
// not registered OR the env-var-sourced endpoint id is empty.
// BR-1.12 fail-fast: the adapter MUST NOT proceed with the upstream call.
var ErrUnsupportedModel = errors.New("endpoint id not configured for model")

// EndpointMap is the friendly-id → Volcengine endpoint-id lookup snapshot
// loaded once at process startup via New(env).
type EndpointMap struct {
	byModel map[string]string
}

// Friendly model id constants — anchor the literal strings the gateway
// dispatcher uses (matches `apps/api-gateway/internal/adapterclient.DoubaoProModelID`
// + DoubaoLiteModelID).
const (
	ModelDoubaoPro  = "doubao-pro"
	ModelDoubaoLite = "doubao-lite"
)

// Env-var names — anchor the literal strings the ConfigMap exposes
// (OQ-4.5-2 ratified brand-prefix `DOUBAO_*`).
const (
	EnvDoubaoProEndpointID  = "DOUBAO_PRO_ENDPOINT_ID"
	EnvDoubaoLiteEndpointID = "DOUBAO_LITE_ENDPOINT_ID"
)

// EnvProvider is the read-an-env-var seam tests use to inject values.
// Production wiring uses os.Getenv.
type EnvProvider func(name string) string

// New constructs an EndpointMap from the supplied EnvProvider. Empty / unset
// env-var values populate empty strings in the underlying map; Lookup
// returns ErrUnsupportedModel for empty values (BR-1.12 fail-fast).
//
// Usage:
//
//	em := upstream.New(os.Getenv)     // production
//	em := upstream.New(func(string) string { return "" })  // test (all unset)
func New(env EnvProvider) *EndpointMap {
	if env == nil {
		env = os.Getenv
	}
	return &EndpointMap{
		byModel: map[string]string{
			ModelDoubaoPro:  env(EnvDoubaoProEndpointID),
			ModelDoubaoLite: env(EnvDoubaoLiteEndpointID),
		},
	}
}

// NewFromOS is a convenience wrapper around New(os.Getenv) for the
// startup path.
func NewFromOS() *EndpointMap { return New(os.Getenv) }

// Lookup returns the Volcengine endpoint id for a friendly model id, OR
// ("", ErrUnsupportedModel) if the id is not registered or the env var
// is unset/empty (BR-1.12 fail-fast).
func (m *EndpointMap) Lookup(modelID string) (string, error) {
	if m == nil {
		return "", ErrUnsupportedModel
	}
	epID, ok := m.byModel[modelID]
	if !ok || epID == "" {
		return "", ErrUnsupportedModel
	}
	return epID, nil
}
