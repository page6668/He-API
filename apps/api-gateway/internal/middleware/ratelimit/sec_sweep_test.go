// Story 5.3 ISSUE-009 / 5.3-SEC-001 — 100-request slog discipline sweep.
//
// Single test that fires 50 allowed + 50 denied requests through the
// middleware chain (with a body carrying message content + plaintext-key
// shapes in headers, to maximise leak surface), captures all slog records,
// and regex-asserts:
//
//   - ZERO matches for `he-[A-Za-z0-9]{40,}` (plaintext API-key shape per
//     security.md §8.2 — 40+ char threshold covers the canonical 43-char
//     production keys with a margin)
//   - ZERO matches for `"messages":\[` (chat-completions content shape per
//     BR-3.10 / BR-X.6)
//
// Aggregate complement to UNIT-004 + UNIT-018 (which assert the same on
// single records). Closes Round-1 ISSUE-009.

package ratelimit_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/ratelimit"
)

// TestSEC001_100RequestSlogDiscipline asserts no plaintext-key shape OR
// message-content shape leaks into any of the slog records captured over
// 100 mixed requests (50 allowed + 50 denied) PLUS the post-deduction
// TPMDeduct calls fired against the same key.
func TestSEC001_100RequestSlogDiscipline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100-request sweep in -short mode")
	}

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = client.Close() }()

	var buf bytes.Buffer
	// Debug-level so the allowed-path slog.Debug record is captured too
	// (the production WARN/INFO surface alone would skip the allowed path).
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Generous QPS so the first 50 pass; tiny RPM so the next 50 deny.
	m := ratelimit.New(ratelimit.Config{
		Redis:            client,
		FreeTierDefaults: ratelimit.Ceilings{QPSMax: 100, RPMMax: 50, TPMMax: 60000},
		FailOpenTimeout:  200 * time.Millisecond,
	}, logger)

	apiKeyID := "sec-001-aggregate-key"
	var called atomic.Int32
	handler := m.Wrap(passHandler(&called))

	// 50 allowed + 50 denied. Each request carries:
	//   - a fake "he-..." 50-char plaintext shape on the Authorization header
	//     (the middleware MUST NOT echo it into slog under any code path)
	//   - a JSON body containing `"messages":[{"role":"user","content":"..."}]`
	//     (the middleware MUST NOT log request bodies)
	fakePlaintextKey := "he-" + strings.Repeat("A", 50)
	leakyBody := `{"model":"qwen-max","messages":[{"role":"user","content":"secret prompt body"}]}`

	for i := 0; i < 100; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(leakyBody))
		r.Header.Set("Authorization", "Bearer "+fakePlaintextKey)
		r = r.WithContext(middleware.WithAPIKeyID(r.Context(), apiKeyID))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)

		// Also exercise the post-deduction path with token values that
		// must NOT surface message content (only integer tokens go to
		// slog per BR-3.10).
		if rec.Code == http.StatusOK {
			m.TPMDeduct(context.Background(), apiKeyID, 100)
		}
	}

	out := buf.String()

	// Plaintext API-key shape (he- + 40+ alphanumerics).
	plaintextRe := regexp.MustCompile(`he-[A-Za-z0-9]{40,}`)
	if plaintextRe.MatchString(out) {
		match := plaintextRe.FindString(out)
		t.Errorf("SEC-001: slog contains plaintext-key shape %q over 100 requests; aggregate output bytes=%d", match, len(out))
	}

	// Message-content shape ("messages":[).
	contentRe := regexp.MustCompile(`"messages":\s*\[`)
	if contentRe.MatchString(out) {
		t.Errorf("SEC-001: slog contains \"messages\":[ shape over 100 requests; aggregate output bytes=%d", len(out))
	}

	// Sanity: verify the harness actually exercised both arms (otherwise
	// a vacuous pass — covers a future regression that accidentally
	// short-circuits the request loop).
	if called.Load() < 1 {
		t.Fatalf("SEC-001: no requests reached the inner handler (called=%d) — harness broken", called.Load())
	}
	if !strings.Contains(out, `"outcome":"denied"`) {
		t.Errorf("SEC-001: no denied records in slog output — RPM ceiling never hit?")
	}
}
