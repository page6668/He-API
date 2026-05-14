package recovery

import (
	"errors"
	"strings"
	"testing"
)

// Scenario: 2.4-UNIT-057 — GenerateSet returns 10 distinct codes, each 10 chars from base32 alphabet.
func TestGenerateSet_Properties(t *testing.T) {
	t.Parallel()
	set, err := GenerateSet()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	if len(set) != SetSize {
		t.Fatalf("len=%d want %d", len(set), SetSize)
	}
	seen := make(map[string]struct{}, SetSize)
	for i, c := range set {
		if len(c) != CodeLength {
			t.Errorf("code %d len=%d", i, len(c))
		}
		for _, r := range c {
			if !strings.ContainsRune(Alphabet, r) {
				t.Errorf("code %d has non-alphabet char %q", i, r)
			}
		}
		if _, dup := seen[c]; dup {
			t.Errorf("dup code at %d: %q", i, c)
		}
		seen[c] = struct{}{}
	}
}

// Scenario: 2.4-UNIT-058 — entropy: across 1000 generations, all distinct.
func TestGenerateCode_Entropy(t *testing.T) {
	t.Parallel()
	const N = 1000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		c, _ := GenerateCode()
		if _, dup := seen[c]; dup {
			t.Fatalf("collision at i=%d (%q) — entropy suspect", i, c)
		}
		seen[c] = struct{}{}
	}
}

// Scenario: 2.4-UNIT-059 — Hash + Compare roundtrip; non-matching code rejected.
func TestHash_Compare_Roundtrip(t *testing.T) {
	t.Parallel()
	c, _ := GenerateCode()
	h, err := Hash(c)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !Compare(h, c) {
		t.Fatalf("compare should match")
	}
	wrong, _ := GenerateCode()
	if Compare(h, wrong) {
		t.Fatalf("compare matched wrong code")
	}
}

// Scenario: 2.4-UNIT-060 — Normalize strips hyphens, uppercases, validates length.
func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		err  error
	}{
		{"ABCD-EFG-HJ2", "ABCDEFGHJ2", nil},    // canonical 4-3-3 display form
		{"abcd efg hj2", "ABCDEFGHJ2", nil},    // lowercase + spaces
		{"  ABCDEFGHJ2  ", "ABCDEFGHJ2", nil},  // surrounding whitespace
		{"ABCDEFGHJ2", "ABCDEFGHJ2", nil},      // already canonical
		{"ABCD", "", ErrInvalidFormat},         // too short
		{"ABCDEFGHJKLM", "", ErrInvalidFormat}, // too long
		{"ABCDEFGHJ?", "", ErrInvalidFormat},   // bad char (? stripped → 9 chars → too short)
		{"ABCDEFGHJ1", "", ErrInvalidFormat},   // 1 is not in RFC 4648 base32 (no 0/1/8/9 ambiguity)
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if !errors.Is(err, c.err) {
			t.Errorf("in=%q: err=%v want %v", c.in, err, c.err)
			continue
		}
		if got != c.want {
			t.Errorf("in=%q: got %q want %q", c.in, got, c.want)
		}
	}
}

// Scenario: 2.4-UNIT-061 — Display formats as 4-3-3 with hyphens.
func TestDisplay(t *testing.T) {
	t.Parallel()
	got := Display("ABCDEFGHJ2")
	if got != "ABCD-EFG-HJ2" {
		t.Fatalf("got %q want ABCD-EFG-HJ2", got)
	}
	// Malformed input returns unchanged.
	got = Display("SHORT")
	if got != "SHORT" {
		t.Fatalf("malformed: got %q", got)
	}
}

// Scenario: 2.4-UNIT-062 — Hash rejects wrong-length input.
func TestHash_RejectsWrongLength(t *testing.T) {
	t.Parallel()
	_, err := Hash("SHORT")
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("want ErrInvalidFormat, got %v", err)
	}
}

// Scenario: 2.4-UNIT-106 (benchmark gate) — BenchmarkRecoveryCompare establishes
// per-call latency for bcrypt-compare; Architect Q4 gate: if 10-in-parallel
// p95 > 180ms, fall back to cost=10. Result must be reported in Dev Log.
func BenchmarkRecoveryCompare(b *testing.B) {
	code, _ := GenerateCode()
	hash, _ := Hash(code)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Compare(hash, code)
	}
}
