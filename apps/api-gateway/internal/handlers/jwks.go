package handlers

import (
	"net/http"
)

// JWKSHandler serves GET /.well-known/jwks.json — the public-key advertise
// path that downstream verifiers + third-party clients consume to verify
// JWT signatures (Wright Round 1 Q1 ruling: api-gateway owns this endpoint
// to keep auth-svc internal-only).
//
// P1 scaffold: returns 501 with a phase pointer. P4 (T3, AC3) loads the public
// PEM from the K8s ConfigMap `he-api-auth-public-keys` (Terraform-created in
// Story 2.2 T0.5) and emits a JWKS document.
type JWKSHandler struct{}

func NewJWKSHandler() *JWKSHandler { return &JWKSHandler{} }

func (h *JWKSHandler) Serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":{"code":"501_not_implemented","message":"pending P4 (Story 2.2 T3, AC3)"}}`))
}
