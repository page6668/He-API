// Story 5.1 T1.1 — API Key plaintext + key_prefix + key_hash generation.
//
// Algorithm (security.md §8.2 steps 1-3 verbatim):
//
//  1. Read 32 cryptographic random bytes from crypto/rand (NEVER math/rand)
//  2. Base62-encode (canonical alphabet "0-9A-Za-z" — Architect Q8 ratified)
//  3. plaintext = "he-" + base62(32 bytes) ≈ "he-" + 43 chars ≈ 46 chars total
//  4. key_prefix = plaintext[:12] ("he-" + 9 base62 chars) — indexed lookup
//  5. key_hash = bcrypt.GenerateFromPassword(plaintext, cost=12)
//
// Defence-in-depth invariants:
//   - The plaintext is returned ONCE; callers MUST NOT persist it (BR-1.5)
//   - bcrypt cost=12 is HARD-CODED — do NOT use bcrypt.DefaultCost (10)
//   - rand.Reader is injectable (`RandReader` package var) for deterministic
//     test fixtures; production paths use crypto/rand.Reader (the default)
//
// Companion tests: 5.1-UNIT-001..005a — see generate_test.go.
package apikey

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the security.md §8.2 step 3 cost factor — locked at 12 for
// the production CreateApiKey path. NOT bcrypt.DefaultCost (which is 10).
// Calibrated for ~250 ms per hash on the Story-1.3 cluster sizing; the
// rate-limit ceiling (BR-1.10: 10 creates / hour / user) bounds aggregate
// cost so a single user's bcrypt budget per hour is ≤ 2.5s wall-clock.
const BcryptCost = 12

// RandBytes is the entropy budget per generated key (BR-1.4). 32 bytes →
// 2^256 ≈ 1.16×10^77 keyspace; practical collision impossible.
const RandBytes = 32

// PrefixWithHyphen is the "he-" literal that fronts every plaintext key.
// Three bytes; copied into both the plaintext AND the key_prefix slice.
const PrefixWithHyphen = "he-"

// KeyPrefixChars is the size of the indexed `key_prefix` lookup column
// (security.md §8.2 step 4 — "first 8-12 chars"; SM ratifies the upper
// bound 12 per Story-3.2 BR-1.1 cascade). Stored in VARCHAR(16) with slack.
// NOTE: defined separately from the Story-3.2 KeyPrefixLength const (same
// value, kept as two declarations because the Story-3.2 const is consumed
// on the Validate hot path while this one anchors the Generate path —
// changing one without the other is a defect that the package compile
// won't catch).
const KeyPrefixChars = 12

// base62Alphabet matches Go's stdlib `(*big.Int).Text(62)` output AND most
// vendored implementations (Architect Q8 ratified canonical alphabet).
// Lexicographic ordering: digits → uppercase → lowercase.
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RandReader is the entropy source. Production binaries use the package
// default (crypto/rand.Reader). Tests override at TestMain init time to
// produce deterministic fixtures (5.1-UNIT-001) or error-injection
// (5.1-UNIT-005 / 5.1-UNIT-005a).
//
// SECURITY-004 attestation: this variable MUST be the ONLY entropy source
// in the generation path; the package MUST NOT import math/rand.
var RandReader io.Reader = cryptorand.Reader

// ErrShortRead is wrapped when the entropy source returns fewer than
// RandBytes bytes before EOF (5.1-UNIT-005a — defensive). io.ReadFull
// returns io.ErrUnexpectedEOF in this case; we wrap so callers can
// distinguish entropy-exhaustion (transient — retry) from a programming
// bug (the reader was misconfigured).
var ErrShortRead = errors.New("apikey: rand reader returned short read")

// Generate produces (plaintext, keyPrefix, keyHash) per the algorithm
// declared at the top of the file. On any IO or bcrypt error returns a
// wrapped error and EMPTY string outputs (defensive — callers MUST check
// err before touching the strings).
//
// The output plaintext length is bounded:
//
//	len(plaintext) ∈ [3 + 32, 3 + 43]    // 32 ≤ base62 chars ≤ 43
//
// In practice the 32 random bytes always encode to ≥ 42 base62 chars
// (with leading-zero stripping the worst case is 42 — 32 / log₂(62) ≈ 42.7),
// so the typical envelope is [45, 46]; the broader [35, 46] in test asserts
// accommodates worst-case leading-zero stripping.
func Generate() (plaintext, keyPrefix, keyHash string, err error) {
	buf := make([]byte, RandBytes)
	if _, readErr := io.ReadFull(RandReader, buf); readErr != nil {
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			return "", "", "", fmt.Errorf("%w: %w", ErrShortRead, readErr)
		}
		return "", "", "", fmt.Errorf("apikey: read crypto/rand: %w", readErr)
	}

	body := encodeBase62(buf)
	plaintext = PrefixWithHyphen + body

	if len(plaintext) < KeyPrefixChars {
		// Unreachable for 32-byte input but guards future tuning of RandBytes.
		return "", "", "", fmt.Errorf("apikey: generated plaintext shorter than key_prefix (%d < %d)", len(plaintext), KeyPrefixChars)
	}
	keyPrefix = plaintext[:KeyPrefixChars]

	hash, hashErr := bcrypt.GenerateFromPassword([]byte(plaintext), BcryptCost)
	if hashErr != nil {
		return "", "", "", fmt.Errorf("apikey: bcrypt: %w", hashErr)
	}
	keyHash = string(hash)
	return plaintext, keyPrefix, keyHash, nil
}

// encodeBase62 produces the canonical base62 representation of buf using
// the "0-9A-Za-z" alphabet (Architect Q8 ratified). Uses math/big.Int.Text
// internally — produces identical output to a vendored table-based encoder
// for any given input (proven by 5.1-UNIT-001 deterministic fixture).
func encodeBase62(buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	z := new(big.Int).SetBytes(buf)
	// big.Int.Text(62) drops leading zeros (mathematically). For a 32-byte
	// CSPRNG output the leading byte is zero with probability 1/256 — we
	// accept the resulting 1-2 char length variance (the response field
	// is variable-length anyway). Callers should not depend on a fixed length.
	encoded := z.Text(62)
	// stdlib z.Text uses alphabet "0-9a-zA-Z" (lowercase first, then upper).
	// We REMAP to the canonical "0-9A-Za-z" alphabet by case-swapping the
	// letter portion. This keeps the math identical while honouring the
	// Q8 alphabet ratification (lexicographic: digits → upper → lower).
	out := make([]byte, len(encoded))
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		switch {
		case c >= '0' && c <= '9':
			out[i] = c
		case c >= 'a' && c <= 'z':
			// stdlib places lowercase in the 10..35 slot (value 10 → 'a').
			// Canonical Q8 places uppercase there → remap a → A.
			out[i] = c - 'a' + 'A'
		case c >= 'A' && c <= 'Z':
			// stdlib places uppercase in the 36..61 slot. Canonical Q8
			// places lowercase there → remap A → a.
			out[i] = c - 'A' + 'a'
		default:
			// big.Int.Text(62) never emits chars outside [0-9A-Za-z].
			// Defensive — preserve as-is.
			out[i] = c
		}
	}
	return string(out)
}

// alphabetIndex returns the canonical Q8 ordinal (0..61) for c. Used by
// chi-square tests + golden-value assertions. Returns -1 for non-alphabet
// chars — callers should treat that as "skip" not "error" so the
// distribution test can ignore the static "he-" prefix.
func alphabetIndex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return 10 + int(c-'A')
	case c >= 'a' && c <= 'z':
		return 36 + int(c-'a')
	}
	return -1
}

// AlphabetIndex exposes alphabetIndex for the security_test package
// (5.1-SECURITY-004 attestation). Internal callers use the lowercase form.
func AlphabetIndex(c byte) int { return alphabetIndex(c) }
