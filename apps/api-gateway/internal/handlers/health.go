// Package handlers — /health probe handler (Story 3.1, AC1).
//
// HealthHandler is a stateless, allocation-free probe handler suitable for
// kubelet / ALB liveness + readiness checks. The response body is marshalled
// once at construction (BR-1.5 byte-identity, BR-1.6 ≤ 1 ms latency budget);
// per-request work is restricted to method dispatch + header write +
// io.Copy of the cached []byte.
//
// Wright (Architect) Q1 ruling: BR-1.1 — process-liveness only. NO upstream
// auth-svc / notification-svc check, NO Redis, NO DB. Deep-health lands in
// Epic 9.
package handlers

import (
	"encoding/json"
	"net/http"
)

// HealthHandler serves the /health and /healthz probe endpoints.
//
// The version field is supplied by the caller (main.go's `serviceVersion`
// const per BR-1.8) so the handler never hardcodes a literal that could drift
// from the build identity.
type HealthHandler struct {
	// body is the JSON document returned to GET callers. Marshalled once in
	// NewHealthHandler and immutable for the lifetime of the handler — see
	// BR-1.6 (no per-request json.Marshal allocation).
	body []byte
}

// NewHealthHandler returns a HealthHandler that responds with a JSON document
// containing `status`, `service`, and the supplied `version`. The marshal
// happens exactly once at construction.
func NewHealthHandler(version string) *HealthHandler {
	doc := struct {
		Status  string `json:"status"`
		Service string `json:"service"`
		Version string `json:"version"`
	}{
		Status:  "ok",
		Service: "api-gateway",
		Version: version,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		// json.Marshal of a static three-string struct cannot fail in
		// practice. Panicking here keeps the constructor signature
		// allocation-free and surfaces any future regression loudly at
		// process boot, not at first probe.
		panic("handlers: marshal /health body: " + err.Error())
	}
	return &HealthHandler{body: b}
}

// Serve implements the probe contract:
//   - GET   → 200 + cached JSON body + Cache-Control: no-store
//   - HEAD  → 200 + headers only (RFC 9110 §9.3.2)
//   - other → 405 + Allow: GET, HEAD + {"error":"method_not_allowed"}
//
// Per BR-1.3 the handler is mounted on a sibling probeMux that bypasses the
// SecurityHeaders / CSRF chain — only Content-Type + Cache-Control + Allow
// are written here.
func (h *HealthHandler) Serve(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(h.body)
	case http.MethodHead:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":"method_not_allowed"}`))
	}
}
