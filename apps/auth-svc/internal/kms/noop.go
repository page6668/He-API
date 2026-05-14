package kms

import "context"

// NoOp is a passthrough KMSClient used in unit tests where we don't care
// about envelope encryption semantics (we want plaintext stored as-is so
// assertions don't have to round-trip through AES-GCM). NEVER use in
// production — Encrypt / Decrypt are identity functions.
type NoOp struct{}

// NewNoOp returns a NoOp client.
func NewNoOp() *NoOp { return &NoOp{} }

// Encrypt returns plaintext unchanged.
func (NoOp) Encrypt(_ context.Context, plaintext []byte) (Ciphertext, error) {
	cp := make([]byte, len(plaintext))
	copy(cp, plaintext)
	return cp, nil
}

// Decrypt returns ciphertext unchanged.
func (NoOp) Decrypt(_ context.Context, ct Ciphertext) ([]byte, error) {
	cp := make([]byte, len(ct))
	copy(cp, ct)
	return cp, nil
}
