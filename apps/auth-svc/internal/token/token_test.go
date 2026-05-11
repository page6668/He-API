package token_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/auth-svc/internal/token"
)

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

// Scenario: 2.2-UNIT-070
// Generate returns a 64-character string drawn from the base64url charset
// (A-Za-z0-9_-). Distinct calls produce distinct outputs.
func TestGenerate_LengthAndCharset(t *testing.T) {
	t.Parallel()
	tok, err := token.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(tok) != 64 {
		t.Fatalf("len(token) = %d, want 64", len(tok))
	}
	charset := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	if !charset.MatchString(tok) {
		t.Fatalf("token = %q contains chars outside base64url alphabet", tok)
	}
}

func TestGenerate_Uniqueness(t *testing.T) {
	t.Parallel()
	const N = 1024
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		tok, err := token.Generate()
		if err != nil {
			t.Fatalf("Generate (%d): %v", i, err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token in %d samples: %q (CSPRNG broken or insufficient entropy)", N, tok)
		}
		seen[tok] = struct{}{}
	}
}

// Scenario: 2.2-UNIT-071
// Generate uses crypto/rand (not math/rand). Static scan of the package
// source: math/rand MUST NOT be imported.
func TestSource_UsesCryptoRandNotMathRand(t *testing.T) {
	t.Parallel()
	src := readPackageSource(t)
	mathRandImport := regexp.MustCompile(`(?m)^\s*"math/rand"|^\s*math_rand "math/rand"`)
	if mathRandImport.MatchString(src) {
		t.Fatalf("package source imports math/rand — CSPRNG mandate violated")
	}
	if !strings.Contains(src, `"crypto/rand"`) {
		t.Fatalf("package source must import crypto/rand for Generate (2.2-UNIT-071)")
	}
}

// Scenario: 2.2-UNIT-072
// Hash returns lowercase hex of SHA-256(token). Deterministic. 64-char hex
// output. The plaintext token is what flows into the email body; only the
// hash is persisted (TS-CONS-006).
func TestHash_LowercaseHexSha256(t *testing.T) {
	t.Parallel()
	cases := []string{
		"abcdefghij",
		"the-quick-brown-fox",
		strings.Repeat("a", 64),
		"",
	}
	for _, raw := range cases {
		got := token.Hash(raw)
		sum := sha256.Sum256([]byte(raw))
		want := hex.EncodeToString(sum[:])
		if got != want {
			t.Errorf("Hash(%q) = %s, want %s", raw, got, want)
		}
		if len(got) != 64 {
			t.Errorf("Hash(%q) length = %d, want 64", raw, len(got))
		}
		if got != strings.ToLower(got) {
			t.Errorf("Hash(%q) = %q, want lowercase hex", raw, got)
		}
	}
}

func TestHash_DifferentInputsDiffer(t *testing.T) {
	t.Parallel()
	a := token.Hash("token-a")
	b := token.Hash("token-b")
	if a == b {
		t.Fatalf("Hash collision on trivial inputs: %s", a)
	}
}

// Scenario: 2.2-UNIT-073
// ParseFormat rejects empty, wrong-length, and outside-charset tokens; nil
// for the canonical 64-char base64url form.
func TestParseFormat_ValidatesShape(t *testing.T) {
	t.Parallel()
	good, err := token.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := token.ParseFormat(good); err != nil {
		t.Fatalf("ParseFormat(valid) = %v, want nil", err)
	}

	bad := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"too_short_63", strings.Repeat("a", 63)},
		{"too_long_65", strings.Repeat("a", 65)},
		{"contains_plus", strings.Repeat("a", 63) + "+"},  // '+' is base64-standard, NOT base64url
		{"contains_slash", strings.Repeat("a", 63) + "/"}, // '/' is base64-standard
		{"contains_equals", strings.Repeat("a", 63) + "="},
		{"contains_dot", strings.Repeat("a", 63) + "."},
		{"contains_space", strings.Repeat("a", 63) + " "},
		{"unicode", strings.Repeat("a", 63) + "ß"},
	}
	for _, c := range bad {
		if err := token.ParseFormat(c.in); !errors.Is(err, token.ErrInvalidFormat) {
			t.Errorf("ParseFormat(%s, %q) = %v, want ErrInvalidFormat", c.name, c.in, err)
		}
	}
}

// Scenario: 2.2-UNIT-074
// Store issues `SET auth:email_verify:{hash(t)} <JSON> EX 86400` with the
// JSON payload {user_id, expires_at, attempts:0}. The plaintext token is
// NEVER written to Redis — the key is derived from token.Hash(t)
// (TS-CONS-006).
func TestStore_SetsHashedKeyWithPayloadAndTTL(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	tok, err := token.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	userID := uuid.New()
	before := time.Now()
	if err := token.Store(ctx, rdb, tok, userID); err != nil {
		t.Fatalf("Store: %v", err)
	}
	after := time.Now()

	// 1. Key MUST be derived from Hash(token), not the plaintext.
	hashedKey := "auth:email_verify:" + token.Hash(tok)
	rawKey := "auth:email_verify:" + tok
	if !mr.Exists(hashedKey) {
		t.Fatalf("Redis missing hashed key %q after Store", hashedKey)
	}
	if mr.Exists(rawKey) {
		t.Fatalf("Redis contains plaintext-token key %q — TS-CONS-006 violation", rawKey)
	}

	// 2. TTL MUST be 24h ±5s tolerance.
	ttl := mr.TTL(hashedKey)
	const want = 24 * time.Hour
	if ttl < want-5*time.Second || ttl > want+5*time.Second {
		t.Fatalf("TTL = %v, want ~%v (±5s)", ttl, want)
	}

	// 3. Payload MUST be JSON {user_id, expires_at, attempts:0}.
	raw, err := mr.Get(hashedKey)
	if err != nil {
		t.Fatalf("miniredis Get: %v", err)
	}
	var payload token.Payload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v\nraw: %s", err, raw)
	}
	if payload.UserID != userID {
		t.Fatalf("payload.UserID = %s, want %s", payload.UserID, userID)
	}
	if payload.Attempts != 0 {
		t.Fatalf("payload.Attempts = %d, want 0 (freshly issued token)", payload.Attempts)
	}
	if payload.ExpiresAt.Before(before.Add(23*time.Hour+59*time.Minute)) ||
		payload.ExpiresAt.After(after.Add(24*time.Hour+1*time.Minute)) {
		t.Fatalf("payload.ExpiresAt = %v, want ~NOW+24h (window %v..%v)",
			payload.ExpiresAt, before.Add(24*time.Hour), after.Add(24*time.Hour))
	}

	// 4. JSON keys MUST be the snake_case names contracted with the rest of
	// auth-svc (user_id, expires_at, attempts) — the static-shape check is
	// what audit + Consume rely on across processes / future Go versions.
	var asMap map[string]any
	if err := json.Unmarshal([]byte(raw), &asMap); err != nil {
		t.Fatalf("payload not JSON object: %v", err)
	}
	for _, key := range []string{"user_id", "expires_at", "attempts"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("payload JSON missing required key %q (got fields %v)", key, asMap)
		}
	}
}

func TestStore_OverwritesExistingKey(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()

	tok, _ := token.Generate()
	u1 := uuid.New()
	u2 := uuid.New()

	if err := token.Store(ctx, rdb, tok, u1); err != nil {
		t.Fatalf("Store(u1): %v", err)
	}
	if err := token.Store(ctx, rdb, tok, u2); err != nil {
		t.Fatalf("Store(u2): %v", err)
	}
	raw, err := rdb.Get(ctx, "auth:email_verify:"+token.Hash(tok)).Result()
	if err != nil {
		t.Fatalf("Get after second Store: %v", err)
	}
	var payload token.Payload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if payload.UserID != u2 {
		t.Fatalf("after overwrite, UserID = %s, want %s", payload.UserID, u2)
	}
}

// Scenario: P3a / Store reverse-index addition
// Store MUST write BOTH the primary hash key AND the reverse index
// `auth:email_verify:user:{user_id}` → token_hash with the SAME TTL.
// The reverse index is how ResendVerification looks up the user's
// currently-active token to invalidate it (BR-1.6).
func TestStore_WritesReverseIndex(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	tok, _ := token.Generate()
	userID := uuid.New()
	if err := token.Store(ctx, rdb, tok, userID); err != nil {
		t.Fatalf("Store: %v", err)
	}

	primaryKey := "auth:email_verify:" + token.Hash(tok)
	reverseKey := "auth:email_verify:user:" + userID.String()

	if !mr.Exists(primaryKey) {
		t.Fatalf("primary key %q missing", primaryKey)
	}
	if !mr.Exists(reverseKey) {
		t.Fatalf("reverse-index key %q missing", reverseKey)
	}
	// Reverse-index value = token hash (so ResendVerification can DEL the primary).
	revVal, _ := mr.Get(reverseKey)
	if revVal != token.Hash(tok) {
		t.Errorf("reverse[%s] = %q, want token hash %q", reverseKey, revVal, token.Hash(tok))
	}
	// Both keys MUST share the same TTL.
	pTTL := mr.TTL(primaryKey)
	rTTL := mr.TTL(reverseKey)
	if pTTL == 0 || rTTL == 0 {
		t.Fatalf("TTLs not set: primary=%v reverse=%v", pTTL, rTTL)
	}
	if diff := pTTL - rTTL; diff < -2*time.Second || diff > 2*time.Second {
		t.Errorf("TTL drift between primary (%v) and reverse (%v) — must be equal within 2s", pTTL, rTTL)
	}
}

// Scenario: 2.2-UNIT-075
// Consume retrieves the payload + DEL's the primary key on success.
// Returns (Payload, nil) with the original user_id.
func TestConsume_HappyPathDelsPrimaryAndReverse(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	tok, _ := token.Generate()
	userID := uuid.New()
	_ = token.Store(ctx, rdb, tok, userID)
	primaryKey := "auth:email_verify:" + token.Hash(tok)
	reverseKey := "auth:email_verify:user:" + userID.String()

	payload, err := token.Consume(ctx, rdb, tok)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if payload.UserID != userID {
		t.Errorf("Consume payload.UserID = %s, want %s", payload.UserID, userID)
	}
	if mr.Exists(primaryKey) {
		t.Errorf("primary key still exists after Consume — one-shot consumption violated")
	}
	if mr.Exists(reverseKey) {
		t.Errorf("reverse index still exists after Consume — should be DEL'd too")
	}
}

// Scenario: 2.2-UNIT-076
// Consume on a missing key (TTL elapsed or never issued) returns
// ErrTokenNotFound.
func TestConsume_NotFoundOnMissingKey(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()
	tok, _ := token.Generate()
	_, err := token.Consume(ctx, rdb, tok)
	if !errors.Is(err, token.ErrTokenNotFound) {
		t.Fatalf("Consume(missing) = %v, want ErrTokenNotFound", err)
	}
}

// One-shot guarantee: second Consume against the same plaintext token
// returns ErrTokenNotFound because the first call DEL'd the primary.
// (BR-2.1)
func TestConsume_SecondCallReturnsNotFound(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()
	tok, _ := token.Generate()
	userID := uuid.New()
	_ = token.Store(ctx, rdb, tok, userID)

	if _, err := token.Consume(ctx, rdb, tok); err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	_, err := token.Consume(ctx, rdb, tok)
	if !errors.Is(err, token.ErrTokenNotFound) {
		t.Fatalf("second Consume = %v, want ErrTokenNotFound (one-shot)", err)
	}
}

// Scenario: 2.2-UNIT-077
// When the stored attempts counter has reached MaxConsumeAttempts-1 the
// next Consume bumps it to the cap, DEL's both keys, and returns
// ErrTokenAttemptsExceeded. Subsequent calls return ErrTokenNotFound.
//
// Simulated by manually pre-writing the payload via mr.Set with
// attempts:4. The Lua eval reads, increments to 5, hits the cap, DEL's.
func TestConsume_AttemptsExceededTriggersLockoutAndDel(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	tok, _ := token.Generate()
	userID := uuid.New()
	primaryKey := "auth:email_verify:" + token.Hash(tok)
	reverseKey := "auth:email_verify:user:" + userID.String()
	// Pre-populate the primary with attempts=4 (one increment away from the cap).
	rawPayload, _ := json.Marshal(token.Payload{
		UserID:    userID,
		ExpiresAt: time.Now().Add(time.Hour),
		Attempts:  4,
	})
	mr.Set(primaryKey, string(rawPayload))
	mr.Set(reverseKey, token.Hash(tok))

	_, err := token.Consume(ctx, rdb, tok)
	if !errors.Is(err, token.ErrTokenAttemptsExceeded) {
		t.Fatalf("Consume(attempts=4→5) = %v, want ErrTokenAttemptsExceeded", err)
	}
	if mr.Exists(primaryKey) {
		t.Errorf("primary key still exists after attempts-exceeded DEL")
	}
	if mr.Exists(reverseKey) {
		t.Errorf("reverse index still exists after attempts-exceeded DEL")
	}
}

// Scenario: P3a / 1 — DeleteForUser uses the reverse index to DEL both keys.
func TestDeleteForUser_RemovesPrimaryAndReverse(t *testing.T) {
	t.Parallel()
	rdb, mr := newRedis(t)
	ctx := context.Background()

	tok, _ := token.Generate()
	userID := uuid.New()
	_ = token.Store(ctx, rdb, tok, userID)

	deleted, err := token.DeleteForUser(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("DeleteForUser: %v", err)
	}
	if !deleted {
		t.Errorf("DeleteForUser returned false, want true (token was active)")
	}
	if mr.Exists("auth:email_verify:" + token.Hash(tok)) {
		t.Errorf("primary key still exists after DeleteForUser")
	}
	if mr.Exists("auth:email_verify:user:" + userID.String()) {
		t.Errorf("reverse index still exists after DeleteForUser")
	}
}

// DeleteForUser on a user with no active token returns (false, nil) — used
// by ResendVerification to know whether anything was invalidated.
func TestDeleteForUser_NoActiveTokenReturnsFalse(t *testing.T) {
	t.Parallel()
	rdb, _ := newRedis(t)
	ctx := context.Background()
	userID := uuid.New()
	deleted, err := token.DeleteForUser(ctx, rdb, userID)
	if err != nil {
		t.Fatalf("DeleteForUser: %v", err)
	}
	if deleted {
		t.Errorf("DeleteForUser returned true on no-active-token user")
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
