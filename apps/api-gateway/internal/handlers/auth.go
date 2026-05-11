// Package handlers exposes the REST handler funcs for the /v1/auth/* surface
// (Wright Round 1 Q1 ruling). Each handler reverse-proxies the upstream
// AuthService gRPC RPC + translates errors to the OpenAI-compatible
// {error:{code,message,he_request_id}} envelope (TS-CONS-014).
//
// P1 scaffold: every handler returns 501 with the phase pointer. P2-P5 wire
// the upstream auth-svc Connect client, error translation, Set-Cookie builder
// (BR-3.7), and middleware chains.
package handlers

import (
	"net/http"
)

type AuthProxy struct{}

func NewAuthProxy() *AuthProxy { return &AuthProxy{} }

func writeNotImplemented(w http.ResponseWriter, phase string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	// Intentionally minimal — error envelope (per TS-CONS-014) lands with the
	// real handlers in P2-P5.
	_, _ = w.Write([]byte(`{"error":{"code":"501_not_implemented","message":"pending ` + phase + `"}}`))
}

func (p *AuthProxy) Signup(w http.ResponseWriter, _ *http.Request) {
	writeNotImplemented(w, "P2 (Story 2.2 T1, AC1)")
}

func (p *AuthProxy) VerifyEmail(w http.ResponseWriter, _ *http.Request) {
	writeNotImplemented(w, "P3 (Story 2.2 T2, AC2)")
}

func (p *AuthProxy) ResendVerification(w http.ResponseWriter, _ *http.Request) {
	writeNotImplemented(w, "P3 (Story 2.2 T2, AC2)")
}

func (p *AuthProxy) Signin(w http.ResponseWriter, _ *http.Request) {
	writeNotImplemented(w, "P4 (Story 2.2 T3, AC3)")
}

func (p *AuthProxy) Refresh(w http.ResponseWriter, _ *http.Request) {
	writeNotImplemented(w, "P4 (Story 2.2 T3, AC3)")
}
