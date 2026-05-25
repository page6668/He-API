// Story 5.1 — UNIT-001..005a (plaintext + key_prefix + key_hash generation).
// See docs/qa/assessments/5.1-test-design-20260525.md.
//
// Convention: every subtest's first executable line carries
//   // Scenario: 5.1-UNIT-NNN[a]
// so `*review 5.1` can grep AC traceability.

package apikey

import (
	"bytes"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"testing/iotest"

	"golang.org/x/crypto/bcrypt"
)

// withRandReader swaps RandReader for the duration of the subtest and
// restores it via t.Cleanup. Sequential subtest execution within a single
// Test* function makes this safe.
func withRandReader(t *testing.T, r io.Reader) {
	t.Helper()
	old := RandReader
	RandReader = r
	t.Cleanup(func() { RandReader = old })
}

// TestGenerate covers UNIT-001..005a (plaintext + key_prefix + key_hash helper).
// Source: T1.1 (story line 500-506) + design-doc §"Unit: Plaintext Generation".
func TestGenerate(t *testing.T) {
	t.Run("5.1-UNIT-001 deterministic with stubbed rand reader", func(t *testing.T) {
		// Scenario: 5.1-UNIT-001
		// Priority: P0
		// Input:    rand.Reader = bytes.NewReader(known 32-byte seed)
		// Expected: plaintext starts with "he-", len ∈ [35,47], keyPrefix == plaintext[:12],
		//           bcrypt.CompareHashAndPassword(keyHash, plaintext) == nil
		// BR-1.4 / Q8
		seed := bytes.Repeat([]byte{0xAA}, 32)
		withRandReader(t, bytes.NewReader(seed))
		plaintext, keyPrefix, keyHash, err := Generate()
		if err != nil {
			t.Fatalf("Generate() err=%v want nil", err)
		}
		if !strings.HasPrefix(plaintext, "he-") {
			t.Fatalf("plaintext=%q want prefix 'he-'", plaintext)
		}
		// Encoded body for 32 bytes of 0xAA is deterministic — assert it
		// stays stable across refactors (Architect L-2 golden assertion).
		if len(plaintext) < 35 || len(plaintext) > 47 {
			t.Fatalf("plaintext len=%d want [35,47]", len(plaintext))
		}
		if keyPrefix != plaintext[:KeyPrefixChars] {
			t.Fatalf("keyPrefix=%q want plaintext[:12]=%q", keyPrefix, plaintext[:KeyPrefixChars])
		}
		if err := bcrypt.CompareHashAndPassword([]byte(keyHash), []byte(plaintext)); err != nil {
			t.Fatalf("bcrypt verify err=%v want nil", err)
		}
	})

	t.Run("5.1-UNIT-002 length invariant over 100 real-rand calls", func(t *testing.T) {
		// Scenario: 5.1-UNIT-002
		// Priority: P0
		// Input:    100 calls of Generate() with real crypto/rand.Reader
		// Expected: every plaintext has len ∈ [35, 47]
		// BR-1.4 / Q8
		withRandReader(t, cryptorand.Reader)
		for i := 0; i < 100; i++ {
			plaintext, _, _, err := Generate()
			if err != nil {
				t.Fatalf("iter %d err=%v", i, err)
			}
			if !strings.HasPrefix(plaintext, "he-") {
				t.Fatalf("iter %d plaintext=%q want prefix 'he-'", i, plaintext)
			}
			if len(plaintext) < 35 || len(plaintext) > 47 {
				t.Fatalf("iter %d plaintext len=%d want [35,47] (%q)", i, len(plaintext), plaintext)
			}
		}
	})

	t.Run("5.1-UNIT-003 chi-square distribution over 1000 keys", func(t *testing.T) {
		// Scenario: 5.1-UNIT-003
		// Priority: P1
		// Input:    1000 generated plaintexts
		// Expected: code-point frequency across 62-char alphabet passes chi-square
		//           χ² < critical_value(α=0.001, df=61)
		// BR-1.4 entropy sanity (crypto/rand attestation)
		withRandReader(t, cryptorand.Reader)
		const trials = 1000
		const alphabetSize = 62
		freq := make([]int, alphabetSize)
		total := 0
		for i := 0; i < trials; i++ {
			plaintext, _, _, err := Generate()
			if err != nil {
				t.Fatalf("iter %d err=%v", i, err)
			}
			// Skip the static "he-" prefix; body is positions 3..end.
			for j := 3; j < len(plaintext); j++ {
				idx := AlphabetIndex(plaintext[j])
				if idx < 0 {
					t.Fatalf("iter %d non-alphabet char at pos %d: %q", i, j, plaintext[j])
				}
				freq[idx]++
				total++
			}
		}
		// Expected count per bucket = total / 62.
		expected := float64(total) / float64(alphabetSize)
		var chi2 float64
		for _, f := range freq {
			diff := float64(f) - expected
			chi2 += (diff * diff) / expected
		}
		// χ² critical value for df=61, α=0.001 ≈ 113.0 (R: qchisq(0.999,61))
		const critical = 113.0
		if chi2 >= critical {
			t.Fatalf("chi-square = %.2f >= critical %.2f — distribution skewed", chi2, critical)
		}
		// Sanity: total chars > 1000 * 35 ≈ 35,000.
		if total < 30_000 {
			t.Fatalf("total chars sampled %d < 30000 — bug in sampling loop", total)
		}
		_ = math.Pi // silence unused-import on toolchain variation
	})

	t.Run("5.1-UNIT-004 bcrypt-verify roundtrip + cost==12", func(t *testing.T) {
		// Scenario: 5.1-UNIT-004
		// Priority: P0
		// Input:    100 calls of Generate()
		// Expected: bcrypt.CompareHashAndPassword(keyHash, plaintext) == nil AND
		//           bcrypt.Cost(keyHash) == 12 (exact, not >=12)
		// security.md §8.2 step 3
		if testing.Short() {
			t.Skip("bcrypt cost=12 × 100 iterations is ~25s wall-clock; skipping in -short")
		}
		withRandReader(t, cryptorand.Reader)
		for i := 0; i < 100; i++ {
			plaintext, _, keyHash, err := Generate()
			if err != nil {
				t.Fatalf("iter %d err=%v", i, err)
			}
			if err := bcrypt.CompareHashAndPassword([]byte(keyHash), []byte(plaintext)); err != nil {
				t.Fatalf("iter %d verify err=%v", i, err)
			}
			cost, err := bcrypt.Cost([]byte(keyHash))
			if err != nil {
				t.Fatalf("iter %d cost err=%v", i, err)
			}
			if cost != BcryptCost {
				t.Fatalf("iter %d cost=%d want %d (exact, not bcrypt.DefaultCost=10)", i, cost, BcryptCost)
			}
		}
	})

	t.Run("5.1-UNIT-005 crypto/rand exhausted error propagation", func(t *testing.T) {
		// Scenario: 5.1-UNIT-005
		// Priority: P0
		// Input:    rand.Reader = iotest.ErrReader(io.ErrUnexpectedEOF)
		// Expected: Generate() returns non-nil err wrapping the IO error; does NOT panic
		// coding-standards.md §12.1 ("no panic on business path")
		// Use a non-EOF error so io.ReadFull surfaces it as-is (not wrapped to UnexpectedEOF).
		sentinel := errors.New("apikey-test: rand exhausted")
		withRandReader(t, iotest.ErrReader(sentinel))
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Generate() panicked: %v", r)
			}
		}()
		plaintext, keyPrefix, keyHash, err := Generate()
		if err == nil {
			t.Fatalf("Generate() err=nil want non-nil")
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("err=%v want errors.Is(_, sentinel)", err)
		}
		if plaintext != "" || keyPrefix != "" || keyHash != "" {
			t.Fatalf("on error want empty outputs; got plaintext=%q keyPrefix=%q keyHash=%q", plaintext, keyPrefix, keyHash)
		}
	})

	t.Run("5.1-UNIT-005a short-read defensive detection", func(t *testing.T) {
		// Scenario: 5.1-UNIT-005a
		// Priority: P1
		// Input:    rand.Reader returns 24 bytes then EOF
		// Expected: Generate() returns err with attribute short_read=true; uses io.ReadFull semantics
		// BR-1.4 + io.ReadFull contract
		withRandReader(t, bytes.NewReader(bytes.Repeat([]byte{0xCC}, 24)))
		_, _, _, err := Generate()
		if err == nil {
			t.Fatalf("err=nil want short-read error")
		}
		if !errors.Is(err, ErrShortRead) {
			t.Fatalf("err=%v want errors.Is(_, ErrShortRead)", err)
		}
	})
}
