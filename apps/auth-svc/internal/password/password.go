package password

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the invariant work factor for every password hash this service
// produces (TS-CONS-001 + BR-1.2). Changing this constant breaks the contract
// inherited by Epic 5 (API Key bcrypt also pins cost=12). The runtime SLO is
// ≤ 250 ms per Hash/Compare on a typical 2-vCPU pod.
const BcryptCost = 12

// ErrPasswordTooShort is returned by ValidateLength when len(pw) < 10.
// (NIST SP 800-63B §5.1.1.2 — minimum length, no character-class enforcement.)
var ErrPasswordTooShort = errors.New("password: too short (minimum 10 characters)")

// Hash returns the bcrypt cost=12 hash of pw and zero-fills pw before
// returning (TS-CONS-005 memory-wipe contract). The caller MUST hold no other
// reference to the same backing array — Hash mutates pw in place.
func Hash(pw []byte) ([]byte, error) {
	defer wipe(pw)
	return bcrypt.GenerateFromPassword(pw, BcryptCost)
}

// Compare wraps bcrypt.CompareHashAndPassword. Returns nil on match,
// bcrypt.ErrMismatchedHashAndPassword on mismatch. The bcrypt routine is
// timing-safe over the hash compare; total wall time is dominated by the
// bcrypt cost factor itself (BR-3.1).
func Compare(hash, pw []byte) error {
	return bcrypt.CompareHashAndPassword(hash, pw)
}

// ValidateLength enforces the NIST SP 800-63B §5.1.1.2 minimum-length rule
// (≥ 10 chars). It does NOT enforce uppercase / digit / symbol classes —
// those policies actively reduce entropy by pushing users toward predictable
// patterns (BR-1.3).
func ValidateLength(pw []byte) error {
	if len(pw) < 10 {
		return ErrPasswordTooShort
	}
	return nil
}

// wipe zero-fills b. Used via defer in any function that holds a plaintext
// password locally (TS-CONS-005).
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
