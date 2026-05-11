package handlers

import (
	"net/http"
)

// JWKSHandler serves `GET /.well-known/jwks.json` — the public-key
// advertise path consumed by downstream JWT verifiers + third-party
// clients (Wright Round 1 Q1 ruling: api-gateway owns this endpoint so
// auth-svc stays internal-only).
//
// The JWKS payload is static after key load — cmd/server pre-computes
// the bytes from the same public-key PEM auth-svc uses, and the handler
// just serves them with the right Content-Type + a long Cache-Control.
//
// Key rotation: a future key-rotation Story will accept a sequence of
// (kid, public_key) pairs and emit one entry per kid in the JWKS — the
// client's `kid` header in the JWS guides which entry to use. For Story
// 2.2 we ship a single-key JWKS.
type JWKSHandler struct {
	Body []byte
}

// NewJWKSHandler constructs a JWKSHandler with the pre-computed JWKS
// bytes. cmd/server passes the result of jwt.Verifier.JWKS(). Tests
// pass a fixed payload to exercise the headers + body shape.
func NewJWKSHandler(body []byte) *JWKSHandler {
	return &JWKSHandler{Body: body}
}

// Serve writes the static JWKS document. Cache-Control allows browsers
// + intermediate caches to keep the payload around for an hour — a key
// rotation invalidates this by emitting a new kid, which the JWS header
// communicates to verifiers regardless of cache state.
func (h *JWKSHandler) Serve(w http.ResponseWriter, _ *http.Request) {
	if len(h.Body) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"503_jwks_unavailable","message":"JWKS not loaded"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/jwk-set+json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(h.Body)
}
