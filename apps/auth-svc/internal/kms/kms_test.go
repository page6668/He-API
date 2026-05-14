package kms

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"testing"
)

func mustMasterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, MasterKeyBytes)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

// Scenario: 2.4-UNIT-091 — AES-256-GCM roundtrip with nonce-prefixed ciphertext layout.
func TestLocal_EncryptDecryptRoundtrip(t *testing.T) {
	t.Parallel()
	k, err := NewLocal(mustMasterKey(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	plaintext := []byte("twenty-byte-totp-secr")
	ct, err := k.Encrypt(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct) < nonceLen+16 {
		t.Fatalf("ct too short: %d", len(ct))
	}
	pt, err := k.Decrypt(context.Background(), ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, plaintext) {
		t.Fatalf("roundtrip mismatch: got %q want %q", pt, plaintext)
	}
}

// Scenario: 2.4-UNIT-092 — 96-bit nonce uniqueness across 10,000 encrypt calls.
// AES-GCM is only sound when nonces never repeat under the same key; crypto/rand
// gives birthday-collision odds of ~1e-15 across 10k samples for a 96-bit nonce.
func TestLocal_NonceUniqueness(t *testing.T) {
	t.Parallel()
	k, err := NewLocal(mustMasterKey(t))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	const N = 10_000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		ct, err := k.Encrypt(context.Background(), []byte("x"))
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		nonceHex := string(ct[:nonceLen])
		if _, dup := seen[nonceHex]; dup {
			t.Fatalf("nonce collision at i=%d", i)
		}
		seen[nonceHex] = struct{}{}
	}
}

// Scenario: 2.4-UNIT-093 — wrong master key → ErrAuthFailed on Decrypt.
func TestLocal_WrongKeyAuthFailure(t *testing.T) {
	t.Parallel()
	a, _ := NewLocal(mustMasterKey(t))
	b, _ := NewLocal(mustMasterKey(t))
	ct, err := a.Encrypt(context.Background(), []byte("secret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = b.Decrypt(context.Background(), ct)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

// Scenario: 2.4-UNIT-094 — constructor rejects keys ≠ 32 bytes.
func TestNewLocal_RejectsWrongKeyLength(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 16, 24, 31, 33, 64} {
		_, err := NewLocal(make([]byte, n))
		if !errors.Is(err, ErrKMSUnavailable) {
			t.Fatalf("n=%d: want ErrKMSUnavailable, got %v", n, err)
		}
	}
}

// Scenario: 2.4-UNIT-095 — tamper detection: flip one bit in ciphertext body.
func TestLocal_TamperDetection(t *testing.T) {
	t.Parallel()
	k, _ := NewLocal(mustMasterKey(t))
	ct, _ := k.Encrypt(context.Background(), []byte("important"))
	// Flip a bit in the body (after the nonce prefix, before the tag).
	ct[nonceLen+1] ^= 0x01
	_, err := k.Decrypt(context.Background(), ct)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed on tamper, got %v", err)
	}
}

// Scenario: 2.4-UNIT-096 — malformed ciphertext (too short) → ErrInvalidCiphertext.
func TestLocal_InvalidCiphertext(t *testing.T) {
	t.Parallel()
	k, _ := NewLocal(mustMasterKey(t))
	_, err := k.Decrypt(context.Background(), []byte("short"))
	if !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("want ErrInvalidCiphertext, got %v", err)
	}
}

// Scenario: 2.4-UNIT-091 (interface) — NoOp satisfies KMSClient and roundtrips identity.
func TestNoOp_Roundtrip(t *testing.T) {
	t.Parallel()
	var k KMSClient = NewNoOp()
	ct, err := k.Encrypt(context.Background(), []byte("plain"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !bytes.Equal(ct, []byte("plain")) {
		t.Fatalf("noop should be identity, got %q", ct)
	}
	pt, err := k.Decrypt(context.Background(), ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(pt, []byte("plain")) {
		t.Fatalf("noop decrypt mismatch: %q", pt)
	}
}
