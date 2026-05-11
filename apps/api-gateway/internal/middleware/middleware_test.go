package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// --- SecurityHeaders tests ---------------------------------------------

func newDownstream(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// Scenario: 2.2-UNIT-190
// SecurityHeaders emits Strict-Transport-Security on every response.
func TestSecurityHeaders_HSTS(t *testing.T) {
	t.Parallel()
	h := middleware.SecurityHeaders(newDownstream(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	got := rr.Header().Get("Strict-Transport-Security")
	if !strings.Contains(got, "max-age=63072000") {
		t.Errorf("HSTS = %q, missing max-age=63072000", got)
	}
	if !strings.Contains(got, "includeSubDomains") {
		t.Errorf("HSTS = %q, missing includeSubDomains", got)
	}
}

// Scenario: 2.2-UNIT-191
// X-Content-Type-Options: nosniff on every response.
func TestSecurityHeaders_NoSniff(t *testing.T) {
	t.Parallel()
	h := middleware.SecurityHeaders(newDownstream(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// Scenario: 2.2-UNIT-192
// X-Frame-Options: DENY on every response.
func TestSecurityHeaders_FrameDeny(t *testing.T) {
	t.Parallel()
	h := middleware.SecurityHeaders(newDownstream(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

// Scenario: 2.2-UNIT-193
// Referrer-Policy: strict-origin-when-cross-origin on every response.
func TestSecurityHeaders_ReferrerPolicy(t *testing.T) {
	t.Parallel()
	h := middleware.SecurityHeaders(newDownstream(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rr.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
}

// Scenario: 2.2-UNIT-194
// Content-Security-Policy non-empty (baseline; tightened in Epic 3+).
func TestSecurityHeaders_CSP(t *testing.T) {
	t.Parallel()
	h := middleware.SecurityHeaders(newDownstream(t))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	got := rr.Header().Get("Content-Security-Policy")
	if got == "" {
		t.Errorf("CSP header missing")
	}
	if !strings.Contains(got, "default-src 'self'") {
		t.Errorf("CSP = %q, missing default-src 'self'", got)
	}
}

// Headers MUST also fire on non-200 responses. Tests with a downstream
// that returns 500.
func TestSecurityHeaders_PresentOn500(t *testing.T) {
	t.Parallel()
	downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := middleware.SecurityHeaders(downstream)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Header().Get("Strict-Transport-Security") == "" {
		t.Errorf("HSTS missing on 500 response")
	}
}

// --- CSRF tests --------------------------------------------------------

// Scenario: 2.2-UNIT-195 — Origin allowlist.
// Allowed origin (exact subdomain match) → flows through.
func TestCSRF_AllowedOriginExact(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{"http://localhost:3000"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", strings.NewReader("{}"))
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// Suffix match: .he-api.com allows console.he-api.com.
func TestCSRF_AllowedOriginSuffixMatch(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	for _, origin := range []string{
		"https://console.he-api.com",
		"https://he-api.com",
		"https://api.he-api.com",
	} {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		req.Header.Set("Origin", origin)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("origin=%s: status = %d, want 200 (suffix-match should pass)", origin, rr.Code)
		}
	}
}

// Scenario: 2.2-UNIT-195 — Foreign origin rejected.
func TestCSRF_ForeignOriginRejected(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for foreign Origin", rr.Code)
	}
}

// Subdomain confusion attempt: .he-api.com MUST NOT match
// evil-he-api.com (suffix without dot separator).
func TestCSRF_SuffixNoConfusionWithSiblingDomain(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	// "evil-he-api.com" superficially ends with "he-api.com" but the
	// suffix match requires "." separator.
	req.Header.Set("Origin", "https://evil-he-api.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — evil-he-api.com must NOT match .he-api.com suffix", rr.Code)
	}
}

// Scenario: 2.2-UNIT-196 — Opaque 403 rejection (no user-readable detail).
func TestCSRF_RejectionBodyIsOpaque(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://evil.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	body := rr.Body.String()
	if !strings.Contains(body, "403_csrf_check_failed") {
		t.Errorf("body should carry the canonical status code; got %q", body)
	}
	// Body MUST NOT carry an attacker-useful "message" or request-id.
	for _, leak := range []string{"origin not allowed", "expected", "allowlist", "subdomain"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("body leaks detail %q: %s", leak, body)
		}
	}
}

// State-mutating verb without Origin → rejected (strict mode).
func TestCSRF_MissingOriginRejected(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil) // no Origin header
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — missing Origin must reject in strict mode", rr.Code)
	}
}

// GET / HEAD / OPTIONS bypass CSRF entirely.
func TestCSRF_SafeMethodsBypass(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/", nil)
		// No Origin set; must still flow through.
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Errorf("method %s rejected by CSRF; safe methods MUST bypass", method)
		}
	}
}

// DryRun mode logs but doesn't block.
func TestCSRF_DryRunPassesThrough(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{
		AllowedOrigins: []string{".he-api.com"},
		DryRun:         true,
	}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://evil.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 in DryRun mode despite foreign Origin", rr.Code)
	}
}

// Empty allowlist + state-mutating verb + Origin → always rejected.
func TestCSRF_EmptyAllowlistAlwaysRejects(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: nil}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "https://he-api.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 with empty allowlist", rr.Code)
	}
}

// Malformed Origin header (no scheme://host) → rejected.
func TestCSRF_MalformedOriginRejected(t *testing.T) {
	t.Parallel()
	cfg := middleware.CSRFConfig{AllowedOrigins: []string{".he-api.com"}}
	h := middleware.CSRF(cfg, newDownstream(t))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Origin", "not-a-url")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for malformed Origin", rr.Code)
	}
}
