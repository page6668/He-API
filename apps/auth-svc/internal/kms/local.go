package kms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// MasterKeyBytes is the required length of the AES-256-GCM master key
// (NIST SP 800-38D). Constructor rejects anything else.
const MasterKeyBytes = 32

// nonceLen is the GCM nonce length (96 bits per NIST SP 800-38D §8.2.1).
// AES-GCM is only cryptographically sound at this nonce length when nonces
// are unique per encrypt — we use crypto/rand for each call (BR-5.3).
const nonceLen = 12

// Local is the bootstrap KMSClient backed by a K8s Secret–delivered master
// key. Ciphertext layout: [12-byte nonce][N-byte ciphertext][16-byte GCM tag]
// — opaque to callers; Decrypt reverses the split.
//
// Concurrency: cipher.AEAD is safe for concurrent use.
type Local struct {
	gcm cipher.AEAD
}

// NewLocal builds the bootstrap KMSClient. `masterKey` MUST be exactly 32
// bytes (AES-256). Returns ErrKMSUnavailable on any constructor failure so
// startup logs surface a deterministic error class.
func NewLocal(masterKey []byte) (*Local, error) {
	if len(masterKey) != MasterKeyBytes {
		return nil, fmt.Errorf("%w: master key must be %d bytes, got %d", ErrKMSUnavailable, MasterKeyBytes, len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("%w: aes cipher: %v", ErrKMSUnavailable, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm wrap: %v", ErrKMSUnavailable, err)
	}
	if gcm.NonceSize() != nonceLen {
		return nil, fmt.Errorf("%w: gcm nonce size mismatch", ErrKMSUnavailable)
	}
	return &Local{gcm: gcm}, nil
}

// Encrypt produces ciphertext = nonce || gcm.Seal(plaintext). Each call
// generates a fresh 96-bit nonce via crypto/rand (NIST SP 800-38D nonce
// uniqueness invariant — BR-5.3).
func (l *Local) Encrypt(_ context.Context, plaintext []byte) (Ciphertext, error) {
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("%w: nonce gen: %v", ErrKMSUnavailable, err)
	}
	// Prefix the nonce; Seal appends ciphertext+tag.
	out := make([]byte, nonceLen, nonceLen+len(plaintext)+l.gcm.Overhead())
	copy(out, nonce)
	out = l.gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Decrypt inverts Encrypt. Returns ErrInvalidCiphertext on length anomalies,
// ErrAuthFailed on tag-mismatch (tamper or wrong master key).
func (l *Local) Decrypt(_ context.Context, ct Ciphertext) ([]byte, error) {
	if len(ct) < nonceLen+l.gcm.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	nonce := ct[:nonceLen]
	body := ct[nonceLen:]
	pt, err := l.gcm.Open(nil, nonce, body, nil)
	if err != nil {
		// crypto/cipher.gcmError is unexported; the canonical aead-tag
		// failure surfaces here. Treat ALL Open failures as auth failure
		// so we never leak distinguishing information across the boundary.
		_ = errors.Unwrap // keep the package self-contained
		return nil, ErrAuthFailed
	}
	return pt, nil
}
