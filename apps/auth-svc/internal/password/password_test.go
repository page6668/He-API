package password_test

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/he-api/he-api/apps/auth-svc/internal/password"
)

// Scenario: 2.2-UNIT-001
// password.Hash uses bcrypt cost = 12 — invariant inherited by Epic 5.
func TestHash_UsesBcryptCost12(t *testing.T) {
	t.Parallel()
	hash, err := password.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	cost, err := bcrypt.Cost(hash)
	if err != nil {
		t.Fatalf("bcrypt.Cost failed: %v", err)
	}
	if cost != password.BcryptCost {
		t.Fatalf("bcrypt cost = %d, want %d", cost, password.BcryptCost)
	}
	if password.BcryptCost != 12 {
		t.Fatalf("BcryptCost constant = %d, want 12 (TS-CONS-001)", password.BcryptCost)
	}
}

// Scenario: 2.2-UNIT-002
// Hash output matches the canonical bcrypt cost=12 prefix regex.
func TestHash_FormatRegex(t *testing.T) {
	t.Parallel()
	hash, err := password.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	re := regexp.MustCompile(`^\$2[ab]\$12\$`)
	if !re.Match(hash) {
		t.Fatalf("hash prefix %q does not match %q", string(hash[:7]), re.String())
	}
}

// Scenario: 2.2-UNIT-003
// Compare returns nil on match, bcrypt.ErrMismatchedHashAndPassword on miss.
func TestCompare_MatchAndMiss(t *testing.T) {
	t.Parallel()
	pw := []byte("correct horse battery staple")
	hash, err := password.Hash(append([]byte(nil), pw...))
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	if err := password.Compare(hash, pw); err != nil {
		t.Fatalf("Compare(match) = %v, want nil", err)
	}
	if err := password.Compare(hash, []byte("wrong password — same length")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("Compare(miss) = %v, want bcrypt.ErrMismatchedHashAndPassword", err)
	}
}

// Scenario: 2.2-UNIT-004
// Compare p99 timing variance for match vs miss < 50ms across 100 iterations.
// BR-3.2 + BR-4.3 — timing-side-channel defense.
func TestCompare_TimingVariance(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	t.Parallel()
	pw := []byte("correct horse battery staple")
	hash, err := password.Hash(append([]byte(nil), pw...))
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	const iters = 100
	matchDurations := make([]float64, iters)
	missDurations := make([]float64, iters)
	miss := []byte("wrong password — same length")
	// Warm-up to settle bcrypt/CPU caches.
	for i := 0; i < 10; i++ {
		_ = password.Compare(hash, pw)
		_ = password.Compare(hash, miss)
	}
	for i := 0; i < iters; i++ {
		start := time.Now()
		_ = password.Compare(hash, pw)
		matchDurations[i] = float64(time.Since(start)) / float64(time.Millisecond)
		start = time.Now()
		_ = password.Compare(hash, miss)
		missDurations[i] = float64(time.Since(start)) / float64(time.Millisecond)
	}
	sort.Float64s(matchDurations)
	sort.Float64s(missDurations)
	p99 := func(s []float64) float64 { return s[int(math.Ceil(0.99*float64(len(s))))-1] }
	matchP99 := p99(matchDurations)
	missP99 := p99(missDurations)
	diff := math.Abs(matchP99 - missP99)
	if diff > 50.0 {
		t.Fatalf("|match p99 - miss p99| = %.2fms (match=%.2fms miss=%.2fms), want < 50ms", diff, matchP99, missP99)
	}
}

// Scenario: 2.2-UNIT-005
// ValidateLength: error when len < 10, nil when >= 10.
func TestValidateLength_Boundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pw      string
		wantErr bool
	}{
		{"", true},
		{"123456789", true},                                                       // 9
		{"1234567890", false},                                                     // 10
		{strings.Repeat("a", 32), false},                                          // long
		{strings.Repeat("x", 1024), false},                                        // very long but well under bcrypt 72
		{strings.Repeat("a", 9), true},                                            // 9
	}
	for _, c := range cases {
		err := password.ValidateLength([]byte(c.pw))
		if c.wantErr && !errors.Is(err, password.ErrPasswordTooShort) {
			t.Fatalf("ValidateLength(len=%d) = %v, want ErrPasswordTooShort", len(c.pw), err)
		}
		if !c.wantErr && err != nil {
			t.Fatalf("ValidateLength(len=%d) = %v, want nil", len(c.pw), err)
		}
	}
}

// Scenario: 2.2-UNIT-006
// ValidateLength does NOT enforce character classes (NIST SP 800-63B §5.1.1.2).
func TestValidateLength_NoCharClassEnforcement(t *testing.T) {
	t.Parallel()
	// All-lowercase, all-digit, all-letter, repeated char — all admit at len 10+.
	cases := []string{
		"aaaaaaaaaa",  // 10x 'a'
		"1111111111",  // 10x '1'
		"abcdefghij",  // letters only
		"0123456789",  // digits only
		"          ",  // spaces only
	}
	for _, pw := range cases {
		if err := password.ValidateLength([]byte(pw)); err != nil {
			t.Fatalf("ValidateLength(%q) = %v, want nil (no class enforcement)", pw, err)
		}
	}
}

// Scenario: 2.2-UNIT-011
// After Hash returns, the caller's password byte slice is zero-filled
// (defer wipe — TS-CONS-005 memory-wipe contract).
func TestHash_WipesCallerSlice(t *testing.T) {
	t.Parallel()
	pw := []byte("correct horse battery staple")
	original := append([]byte(nil), pw...) // capture for sanity diff
	_, err := password.Hash(pw)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	zeros := make([]byte, len(pw))
	if !bytes.Equal(pw, zeros) {
		t.Fatalf("password slice not wiped: got %q, want all-zero (was %q before Hash)", pw, original)
	}
}

// Scenario: 2.2-UNIT-012
// No log/audit/error string format includes plaintext password parameter
// (TS-CONS-005 + BR-1.2). Static regex scan over the package source files.
func TestSourceContainsNoPlaintextPasswordLeak(t *testing.T) {
	t.Parallel()
	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Patterns that would leak: fmt.Sprintf/Errorf/Printf/Println/Fprintf containing pw|password as a verb arg.
	leakPatterns := []*regexp.Regexp{
		// fmt.Errorf("...%s...", pw)  / Sprintf("...%v...", password)
		regexp.MustCompile(`fmt\.(Errorf|Sprintf|Printf|Fprintf|Println|Print)\([^)]*"[^"]*%[svqxXdoebgfEGUcUp][^"]*"[^)]*,\s*(pw|password|plaintext|cleartext)\b`),
		// slog.Info("...", "password", pw)
		regexp.MustCompile(`slog\.(Info|Warn|Error|Debug)\([^)]*"(pw|password|plaintext|cleartext)"\s*,\s*(pw|password|plaintext|cleartext)\b`),
		// errors.New("password is ...")  — accidentally embedding plaintext
		regexp.MustCompile(`errors\.New\("[^"]*"\s*\+\s*string\((pw|password|plaintext|cleartext)\)`),
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		for _, re := range leakPatterns {
			if loc := re.FindIndex(data); loc != nil {
				t.Errorf("%s:%d: plaintext-password leak suspected: %q", name, byteOffsetToLine(data, loc[0]), data[loc[0]:loc[1]])
			}
		}
	}
}

func byteOffsetToLine(b []byte, off int) int {
	if off > len(b) {
		off = len(b)
	}
	return bytes.Count(b[:off], []byte{'\n'}) + 1
}
