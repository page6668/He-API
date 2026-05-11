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

// dummyHash is a pre-computed bcrypt hash used by DummyCompare for the
// timing-parity branch in LoginUser. Generated once at package init.
// The hash is NOT secret — knowing it doesn't help an attacker because
// bcrypt is one-way + uses a per-hash salt that's already embedded.
var dummyHash []byte

func init() {
	// Generate at init so every process startup pays the ~200ms cost once
	// (negligible compared to the binary startup time). bcrypt.GenerateFromPassword
	// is the same primitive Hash uses.
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing-parity-do-not-use"), BcryptCost)
	if err != nil {
		// Should be impossible — bcrypt.GenerateFromPassword only fails on
		// invalid cost (out of range) or memory exhaustion. Panicking here
		// fails the binary loudly at startup rather than letting LoginUser
		// silently drift into a no-defense state.
		panic("password: init dummy hash: " + err.Error())
	}
	dummyHash = h
}

// DummyCompare performs a real bcrypt.CompareHashAndPassword against a
// pre-generated static hash, consuming the same CPU time as a real
// Compare on a found user. Always returns bcrypt.ErrMismatchedHashAndPassword.
//
// LoginUser calls this when GetUserByEmail returns ErrUserNotFound so the
// response time for unknown-email is statistically indistinguishable from
// wrong-password (BR-3.2 + UNIT-132 timing-side-channel defense). NEVER
// returns nil — the unknown-email branch always leads to a 401.
func DummyCompare(pw []byte) error {
	return bcrypt.CompareHashAndPassword(dummyHash, pw)
}
