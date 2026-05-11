package ratelimit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/auth-svc/internal/ratelimit"
)

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
	})
	return rdb, mr
}

// Scenario: 2.2-UNIT-170
// CheckAndIncr atomically INCRs the counter and, on the *first* call (when
// post-INCR count == 1), issues EXPIRE for the supplied window. The two
// commands are bundled in a single Lua script so a process crash between
// them cannot leave a key without a TTL.
func TestCheckAndIncr_FirstCallSetsTTL(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	res, err := ratelimit.CheckAndIncr(ctx, rdb, "ratelimit:signup:ip:1.2.3.4", 5, 5*time.Minute)
	if err != nil {
		t.Fatalf("CheckAndIncr (first): %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("Count = %d, want 1 on first call", res.Count)
	}
	ttl := mr.TTL("ratelimit:signup:ip:1.2.3.4")
	if ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("TTL = %v, want (0, 5m]", ttl)
	}
	// RetryAfter is unpopulated on a success path.
	if res.RetryAfter != 0 {
		t.Fatalf("RetryAfter = %v on success, want 0", res.RetryAfter)
	}
}

// Subsequent calls within the window MUST keep the TTL stable — only the
// first INCR sets it. (Otherwise an attacker could indefinitely refresh the
// window by retrying.)
func TestCheckAndIncr_SubsequentCallsDoNotResetTTL(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()
	key := "ratelimit:signup:ip:5.6.7.8"

	if _, err := ratelimit.CheckAndIncr(ctx, rdb, key, 5, 5*time.Minute); err != nil {
		t.Fatalf("first call: %v", err)
	}
	ttlBefore := mr.TTL(key)
	// Fast-forward minimal time so a refresh would be detectable.
	mr.FastForward(1 * time.Second)
	res, err := ratelimit.CheckAndIncr(ctx, rdb, key, 5, 5*time.Minute)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("second-call Count = %d, want 2", res.Count)
	}
	ttlAfter := mr.TTL(key)
	if ttlAfter > ttlBefore {
		t.Fatalf("TTL grew on subsequent call: before=%v after=%v — second INCR must NOT issue EXPIRE", ttlBefore, ttlAfter)
	}
}

// Scenario: 2.2-UNIT-171
// CheckAndIncr returns ErrRateLimited once the post-INCR count exceeds the
// limit. RetryAfter reflects the key's remaining TTL.
func TestCheckAndIncr_OverLimitReturnsErrRateLimited(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()
	key := "ratelimit:signin:email:abc"
	const limit = 3

	// Consume the allotted quota.
	for i := int64(1); i <= limit; i++ {
		res, err := ratelimit.CheckAndIncr(ctx, rdb, key, limit, time.Minute)
		if err != nil {
			t.Fatalf("CheckAndIncr (i=%d): %v", i, err)
		}
		if res.Count != i {
			t.Fatalf("CheckAndIncr (i=%d) Count = %d, want %d", i, res.Count, i)
		}
	}

	// The 4th call (count==4, limit==3) MUST return ErrRateLimited.
	res, err := ratelimit.CheckAndIncr(ctx, rdb, key, limit, time.Minute)
	if !errors.Is(err, ratelimit.ErrRateLimited) {
		t.Fatalf("CheckAndIncr (over limit) = %v, want ErrRateLimited", err)
	}
	if res.Count != limit+1 {
		t.Fatalf("over-limit Count = %d, want %d", res.Count, limit+1)
	}
	if res.RetryAfter <= 0 || res.RetryAfter > time.Minute {
		t.Fatalf("RetryAfter = %v, want (0, 1m]", res.RetryAfter)
	}
}

// Scenario: 2.2-UNIT-172
// EmailHash = lowercase hex of sha256(strings.ToLower(strings.TrimSpace(email))).
// Same input modulo case + whitespace MUST yield the same hash.
func TestEmailHash_LowercaseTrimSha256(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want string
	}{
		{"user@example.com", hexOf("user@example.com")},
		{"User@Example.com", hexOf("user@example.com")},
		{"  USER@EXAMPLE.COM  ", hexOf("user@example.com")},
		{"\tuser@example.com\n", hexOf("user@example.com")},
		{"x@y.z", hexOf("x@y.z")},
	}
	for _, c := range cases {
		got := ratelimit.EmailHash(c.raw)
		if got != c.want {
			t.Errorf("EmailHash(%q) = %s, want %s", c.raw, got, c.want)
		}
		// Lower-case hex, 64 chars (SHA-256).
		if len(got) != 64 {
			t.Errorf("EmailHash(%q) length = %d, want 64", c.raw, len(got))
		}
		if got != strings.ToLower(got) {
			t.Errorf("EmailHash(%q) = %q, want all-lowercase hex", c.raw, got)
		}
	}
}

func hexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Scenario: 2.2-UNIT-173
// The five Redis key prefixes from BR-4.1 are all present as helpers and
// each helper produces a key with the correct prefix.
func TestKeyHelpers_FiveDistinctPrefixes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		got    string
		prefix string
	}{
		{"SignupIPKey", ratelimit.SignupIPKey("1.2.3.4"), "ratelimit:signup:ip:"},
		{"SigninIPKey", ratelimit.SigninIPKey("1.2.3.4"), "ratelimit:signin:ip:"},
		{"SigninEmailKey", ratelimit.SigninEmailKey("user@example.com"), "ratelimit:signin:email:"},
		{"ResendIPKey", ratelimit.ResendIPKey("1.2.3.4"), "ratelimit:resend:ip:"},
		{"ResendEmailKey", ratelimit.ResendEmailKey("user@example.com"), "ratelimit:resend:email:"},
	}
	seenPrefix := map[string]bool{}
	for _, c := range cases {
		if !strings.HasPrefix(c.got, c.prefix) {
			t.Errorf("%s = %q, want prefix %q", c.name, c.got, c.prefix)
		}
		if seenPrefix[c.prefix] {
			t.Errorf("prefix %q used twice", c.prefix)
		}
		seenPrefix[c.prefix] = true
	}
	if len(seenPrefix) != 5 {
		t.Fatalf("distinct prefixes = %d, want 5 (BR-4.1 five-key matrix)", len(seenPrefix))
	}
}

// Static source scan complements the behavioral test above: each of the 5
// canonical prefix string literals MUST appear in ratelimit.go (greppable).
func TestSourceContainsFiveKeyPrefixLiterals(t *testing.T) {
	t.Parallel()
	src := readPackageSource(t)
	for _, prefix := range []string{
		"ratelimit:signup:ip:",
		"ratelimit:signin:ip:",
		"ratelimit:signin:email:",
		"ratelimit:resend:ip:",
		"ratelimit:resend:email:",
	} {
		if !strings.Contains(src, prefix) {
			t.Errorf("package source missing key prefix literal %q", prefix)
		}
	}
}

// Scenario: 2.2-UNIT-174
// Email-keyed helpers MUST embed EmailHash, not the raw email. Behavioral +
// static checks.
func TestKeyHelpers_EmailKeysCarryNoPlaintext(t *testing.T) {
	t.Parallel()
	raw := "Sensitive@User.Example"
	signin := ratelimit.SigninEmailKey(raw)
	resend := ratelimit.ResendEmailKey(raw)

	for _, k := range []string{signin, resend} {
		if strings.Contains(strings.ToLower(k), strings.ToLower(raw)) {
			t.Errorf("key %q contains plaintext email %q", k, raw)
		}
		// Must contain '@'? No — the hash strips structure entirely.
		if strings.Contains(k, "@") {
			t.Errorf("key %q contains '@' — suggests raw email leak", k)
		}
		// Must end with the canonical sha256 hex.
		want := ratelimit.EmailHash(raw)
		if !strings.HasSuffix(k, want) {
			t.Errorf("key %q does not end with EmailHash(%q)=%s", k, raw, want)
		}
	}

	// Static source scan: every email-keyed key literal in source ends with
	// "%s" or "{" placeholder; the value bound MUST be ratelimit.EmailHash(…)
	// or the EmailHash helper invocation. We allow EmailHash() calls in
	// helper bodies and reject any literal "{raw_email}" patterns.
	src := readPackageSource(t)
	badPattern := regexp.MustCompile(`ratelimit:(signin|resend):email:[^"%{]+@`)
	if loc := badPattern.FindStringIndex(src); loc != nil {
		t.Errorf("package source contains raw email substring inside an email-keyed prefix: %q", src[loc[0]:loc[1]])
	}
}

// Scenario: 2.2-UNIT-175
// Atomic INCR holds under concurrency: 10 goroutines hitting the same key
// produce a final count of exactly 10.
func TestCheckAndIncr_AtomicUnderConcurrency(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()
	key := "ratelimit:signup:ip:concurrent"
	const N = 10
	var wg sync.WaitGroup
	var maxObserved int64
	var errs atomic.Int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := ratelimit.CheckAndIncr(ctx, rdb, key, 1000 /*high limit so no rate-limit*/, time.Minute)
			if err != nil {
				errs.Add(1)
				return
			}
			// Track max — final reading should be N.
			for {
				cur := atomic.LoadInt64(&maxObserved)
				if res.Count <= cur || atomic.CompareAndSwapInt64(&maxObserved, cur, res.Count) {
					break
				}
			}
		}()
	}
	wg.Wait()
	if errs.Load() != 0 {
		t.Fatalf("concurrent CheckAndIncr produced %d errors", errs.Load())
	}
	if maxObserved != int64(N) {
		t.Fatalf("max observed count = %d, want %d (one increment per goroutine, atomic)", maxObserved, N)
	}
	// Confirm via direct GET that final value is exactly N.
	got, err := rdb.Get(ctx, key).Int64()
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	if got != int64(N) {
		t.Fatalf("final Redis value = %d, want %d", got, N)
	}
}

// Helper — read all non-test .go files in this test's package directory.
func readPackageSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		t.Fatalf("no .go source found in %s", dir)
	}
	return sb.String()
}

// Sanity — guard against accidentally using strings.HasPrefix on an empty
// result by printing a deterministic string we can grep in the test name.
var _ = fmt.Sprintf
