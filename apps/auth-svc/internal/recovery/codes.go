// Package recovery generates and verifies single-use 2FA recovery codes.
//
// Wright Round 1 Q3 ruling: RFC 4648 base32 alphabet (A-Z2-7) — matches TOTP
// secret encoding throughout the codebase.
// Wright Round 1 Q4 ruling: bcrypt cost=12 (uniform crypto policy with
// password). Dev-benchmark gate at staging (BenchmarkRecoveryCompare); if
// 10-in-parallel p95 > 180ms, drop to cost=10 (pre-authorized fallback).
//
// Invariants (BR-3.1, BR-3.2, BR-3.4):
//   - Code length: 10 chars, ~50 bits entropy each.
//   - Storage: bcrypt-hashed (plaintext NEVER persists past initial response).
//   - Comparison: bcrypt.CompareHashAndPassword is constant-time by design.
//   - Display format: 4-3-3 with hyphens ("ABCD-EFGH-J2K") — strip on input.
package recovery

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Tunables.
const (
	// CodeLength is the recovery-code character count (BR-3.1). 10 chars
	// from a 32-char alphabet → ~50 bits entropy.
	CodeLength = 10

	// BcryptCost is the bcrypt work factor for recovery code hashing
	// (BR-3.2, Architect Q4). Matches Story 2.2 password cost. Pre-authorized
	// fallback to cost=10 if staging benchmark p95 > 180ms.
	BcryptCost = 12

	// SetSize is the canonical recovery-code batch size (10 codes per
	// enrollment + per regeneration).
	SetSize = 10
)

// Alphabet is the RFC 4648 base32 character set (A-Z2-7) — case-sensitive
// at storage; input is uppercased + non-alphanumeric-stripped before compare.
const Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// ErrInvalidFormat is returned by Normalize when the input cannot be coerced
// into a valid 10-char base32 string.
var ErrInvalidFormat = errors.New("recovery: invalid code format")

// GenerateCode returns one fresh 10-char base32 code from crypto/rand. Each
// character is drawn uniformly from Alphabet (BR-3.1).
func GenerateCode() (string, error) {
	buf := make([]byte, CodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("recovery: rand: %w", err)
	}
	out := make([]byte, CodeLength)
	for i, b := range buf {
		out[i] = Alphabet[int(b)%len(Alphabet)]
	}
	return string(out), nil
}

// GenerateSet returns SetSize fresh codes. Single round-trip to crypto/rand
// keeps allocation tight.
func GenerateSet() ([]string, error) {
	out := make([]string, SetSize)
	for i := 0; i < SetSize; i++ {
		c, err := GenerateCode()
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

// Hash returns the bcrypt hash of `code` at BcryptCost. Callers persist this
// in mfa_recovery_codes.code_hash; plaintext is dropped immediately.
func Hash(code string) (string, error) {
	if len(code) != CodeLength {
		return "", ErrInvalidFormat
	}
	h, err := bcrypt.GenerateFromPassword([]byte(code), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("recovery: bcrypt: %w", err)
	}
	return string(h), nil
}

// Compare returns true iff `code` matches the stored bcrypt hash. Constant-
// time by design (bcrypt.CompareHashAndPassword internal invariant).
func Compare(hash, code string) bool {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)); err != nil {
		return false
	}
	return true
}

// Normalize strips non-alphanumeric (hyphens, whitespace) and uppercases the
// input. Used by the AC3 UseRecoveryCode handler before bcrypt compare —
// users may type with or without the display hyphens ("ABCD-EFGH-J2" vs
// "ABCDEFGHJ2"). Returns ErrInvalidFormat if the result is not exactly
// CodeLength chars from Alphabet.
func Normalize(input string) (string, error) {
	var b strings.Builder
	b.Grow(CodeLength)
	for _, r := range input {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		// Strip everything else (hyphens, whitespace, punctuation).
		}
	}
	out := b.String()
	if len(out) != CodeLength {
		return "", ErrInvalidFormat
	}
	for i := 0; i < len(out); i++ {
		if !strings.ContainsRune(Alphabet, rune(out[i])) {
			return "", ErrInvalidFormat
		}
	}
	return out, nil
}

// Display formats a code for human-readable presentation: groups of 4-3-3
// joined by hyphens ("ABCD-EFGH-J2K"). Per BR-3.4. Input MUST be exactly
// CodeLength chars; returns input unchanged on length mismatch (safest
// fallback for malformed data).
func Display(code string) string {
	if len(code) != CodeLength {
		return code
	}
	return code[0:4] + "-" + code[4:7] + "-" + code[7:10]
}
