// Package password isolates password hashing (bcrypt cost=12), comparison
// (timing-safe + dummy-compare for unknown emails), length validation (≥10
// chars, no character-class enforcement per NIST SP 800-63B §5.1.1.2), and
// HaveIBeenPwned k-anonymity breach checks (fail-closed on outage).
//
// Invariants (TS-CONS-001, TS-CONS-004, TS-CONS-005):
//   - BcryptCost = 12 (compile-time const; never overridden, including in tests)
//   - HIBP outbound URL exposes only SHA-1(password)[0:5]
//   - Plaintext password byte slices wiped via defer before return
//   - Compare timing variance < 50ms p99 across match/mismatch/unknown-user
//
// P2 (T1, AC1) materializes the implementation. P1 holds only this doc file.
package password
