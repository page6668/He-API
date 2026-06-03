// Story 5.2 — SECURITY-006 (PII discipline: no raw IPs in slog output).
package keypolicy_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware/keypolicy"
)

// 5.2-SECURITY-006 — the IP-whitelist denial path logs the client IP as a
// /24+/64-masked SHA-256 hash, NEVER the raw address. A grep of the slog
// output for the raw IP MUST find zero matches.
func TestKeyPolicy_NoRawIPInLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	claims := &middleware.CachedClaims{
		APIKeyID:         "k1",
		UserID:           "u1",
		ScopeIPWhitelist: []string{"192.168.1.0/24"}, // client 203.0.113.77 denied
	}
	h := keypolicy.New(keypolicy.Options{Logger: logger})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen-max"}`))
	r.RemoteAddr = "203.0.113.77:54321"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(middleware.WithCacheValue(context.Background(), claims)))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", w.Code)
	}
	logs := buf.String()
	if strings.Contains(logs, "203.0.113.77") {
		t.Fatalf("RAW IP leaked into slog output:\n%s", logs)
	}
	if !strings.Contains(logs, "client_ip_hash=") {
		t.Fatalf("expected a client_ip_hash field in:\n%s", logs)
	}
}
