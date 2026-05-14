// Package kms isolates envelope encryption for at-rest secrets stored in PG.
// Story 2.4 introduces this abstraction to wrap users.totp_secret_encrypted;
// Story 2.7 (account deletion / data export) and later API-key flows will
// reuse it.
//
// Wright Round 1 Q1 ruling: bootstrap impl is K8s Secret–delivered AES-256-GCM
// master key + per-call 96-bit nonce (NIST SP 800-38D compliant). Production
// target is HashiCorp Vault Transit or Aliyun KMS — both must drop into the
// `KMSClient` interface below without changing call sites.
//
// **Interface stability is load-bearing** (Q1 + Architect §Rec.1):
//   - Encrypt / Decrypt take + return opaque []byte ciphertext.
//   - Implementations control nonce handling internally; callers must never
//     observe or persist the nonce separately.
//   - Caller passes context.Context for cancellation propagation only; no
//     KMS-specific identifiers / key IDs leak across the boundary.
package kms

import (
	"context"
	"errors"
)

// Errors returned by KMS implementations. Callers map these to specific HTTP
// codes (503_kms_unavailable, etc.) in the handler layer.
var (
	// ErrKMSUnavailable indicates the master key store could not be reached
	// or returned a transient failure. Callers should surface 503 (Service
	// Unavailable) and emit a HIGH-severity audit event.
	ErrKMSUnavailable = errors.New("kms: unavailable")

	// ErrAuthFailed indicates AES-GCM authentication tag verification failed
	// — i.e., ciphertext was tampered or the master key changed without a
	// rotation runbook (BR-5.2). Callers MUST treat as a security event.
	ErrAuthFailed = errors.New("kms: authentication failed")

	// ErrInvalidCiphertext indicates the ciphertext blob is malformed (too
	// short to contain a nonce + tag, etc.).
	ErrInvalidCiphertext = errors.New("kms: invalid ciphertext")
)

// Ciphertext is the opaque blob returned by Encrypt and accepted by Decrypt.
// The internal layout is impl-defined; callers MUST persist verbatim.
//
// Defined as an ALIAS (not a newtype) so the kms package and handler-layer
// interfaces — which use []byte for the same role — interoperate without
// explicit conversion. This is a deliberate trade-off: documentation
// strength vs. cross-package interface friction. We chose ergonomics.
type Ciphertext = []byte

// KMSClient is the only interface handlers depend on. local.go provides the
// bootstrap impl; future vault.go / aliyun_kms.go can swap at startup via
// dependency injection in cmd/server/main.go.
//
// Both methods are safe for concurrent use.
type KMSClient interface {
	// Encrypt seals plaintext under the active master key. The returned blob
	// is self-describing (nonce + ciphertext + tag); callers persist verbatim.
	Encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	// Decrypt opens a previously-sealed blob. Returns ErrAuthFailed on tamper
	// or master-key mismatch; ErrInvalidCiphertext on malformed input.
	Decrypt(ctx context.Context, ct []byte) ([]byte, error)
}
