// Story 5.3 — unit tests for the TPM post-deduction hook.

package ratelimit_test

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
)

func newDeducter(t *testing.T) (*miniredis.Miniredis, *ratelimit.Middleware, *bytes.Buffer, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := ratelimit.New(ratelimit.Config{
		Redis:            client,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 10, RPMMax: 300, TPMMax: 60000},
		FailOpenTimeout:  200 * time.Millisecond,
	}, logger)
	return mr, m, &buf, func() { _ = client.Close(); mr.Close() }
}

// 5.3-UNIT-015 — TPMDeduct(tokens=8000) on existing counter=45000
// → counter=53000 + EXPIRE 60 set (NX semantics; no slide on repeat).
func TestUNIT015_TPMDeductBasic(t *testing.T) {
	mr, m, _, cleanup := newDeducter(t)
	defer cleanup()

	apiKeyID := "k-015"
	mr.Set("ratelimit:key:"+apiKeyID+":tpm", "45000")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":tpm", 60*time.Second)

	m.TPMDeduct(context.Background(), apiKeyID, 8000)

	got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm")
	if got != "53000" {
		t.Errorf("TPM counter: want 53000, got %q", got)
	}
	// TTL must still be ~60s (NX preserved).
	ttl := mr.TTL("ratelimit:key:" + apiKeyID + ":tpm")
	if ttl <= 0 || ttl > 61*time.Second {
		t.Errorf("TTL out of range: %v", ttl)
	}
}

// 5.3-UNIT-029 — TPMDeduct(tokens=0) is a no-op (defensive).
func TestUNIT029_TPMDeductZeroIsNoOp(t *testing.T) {
	mr, m, _, cleanup := newDeducter(t)
	defer cleanup()

	apiKeyID := "k-029"
	m.TPMDeduct(context.Background(), apiKeyID, 0)

	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm"); got != "" {
		t.Errorf("TPM counter should be untouched on tokens=0, got %q", got)
	}
}

// 5.3-UNIT-030 — TPMDeduct(tokens=-1) is a defensive no-op + slog WARN.
func TestUNIT030_TPMDeductNegativeNoOp(t *testing.T) {
	mr, m, buf, cleanup := newDeducter(t)
	defer cleanup()

	apiKeyID := "k-030"
	m.TPMDeduct(context.Background(), apiKeyID, -1)

	if got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm"); got != "" {
		t.Errorf("TPM counter should not move on tokens=-1, got %q", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("ratelimit_tpm_deduct_skipped")) {
		t.Errorf("expected slog event ratelimit_tpm_deduct_skipped, got: %s", buf.String())
	}
}

// 5.3-UNIT-017 — TPM race window: 5 concurrent +10000-tokens deductions
// land at counter=105000 (over-shoot=45000). Documents BR-3.4 / BR-3.8
// accepted race semantics.
func TestUNIT017_TPMRaceWindow(t *testing.T) {
	mr, m, _, cleanup := newDeducter(t)
	defer cleanup()

	apiKeyID := "k-017"
	mr.Set("ratelimit:key:"+apiKeyID+":tpm", "55000")
	mr.SetTTL("ratelimit:key:"+apiKeyID+":tpm", 60*time.Second)

	const concurrent = 5
	const tokensPerRequest = 10000
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			m.TPMDeduct(context.Background(), apiKeyID, tokensPerRequest)
		}()
	}
	wg.Wait()

	got := mrGet(t, mr, "ratelimit:key:"+apiKeyID+":tpm")
	want := strconv.Itoa(55000 + concurrent*tokensPerRequest) // 105000
	if got != want {
		t.Errorf("post-deduction counter race-window: want %s, got %q", want, got)
	}
	// Over-shoot is 105000 - 60000 = 45000 tokens. Accepted per BR-3.4.
}

// 5.3-UNIT-018 — slog discipline: integer token count only; NO message
// content (BR-3.10 / BR-X.6).
func TestUNIT018_TPMDeductSlogDiscipline(t *testing.T) {
	_, m, buf, cleanup := newDeducter(t)
	defer cleanup()

	m.TPMDeduct(context.Background(), "k-018", 8000)
	out := buf.String()
	// The slog buffer should NEVER contain anything that looks like
	// message content. The integer "tokens":8000 IS expected.
	if !bytes.Contains([]byte(out), []byte(`"tokens":8000`)) {
		t.Errorf("expected tokens=8000 to appear in slog: %s", out)
	}
	for _, banned := range []string{`"content":`, `"messages":[`} {
		if bytes.Contains([]byte(out), []byte(banned)) {
			t.Errorf("slog leaked content shape %q in: %s", banned, out)
		}
	}
}

// fail-open: Redis nil → TPMDeduct is a no-op (logs DEBUG; no panic).
func TestTPMDeductRedisNilNoOp(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(new(bytes.Buffer), nil))
	m := ratelimit.New(ratelimit.Config{
		Redis:            nil,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 1, RPMMax: 1, TPMMax: 1},
	}, logger)
	// Must not panic.
	m.TPMDeduct(context.Background(), "k-x", 100)
	var _ atomic.Int32 // keep import used
}
