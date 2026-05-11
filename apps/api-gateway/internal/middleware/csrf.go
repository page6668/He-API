// CSRF Origin allowlist middleware (Story 2.2 BR-4.6 + UNIT-195/196).
//
// State-mutating HTTP methods (POST/PUT/PATCH/DELETE) MUST carry an
// Origin header matching the configured allowlist. Origin is harder to
// spoof than Referer (the browser sets it without scripting access) and
// modern browsers send it on every state-mutating same-site + cross-site
// request alike.
//
// On rejection: opaque 403 — NO user-readable message in the response
// (BR-4.6: rejection must be opaque to attacker debugging). The
// rejection IS captured in the audit log (auth.csrf_violation event)
// by the gateway middleware-level audit hook, P5b lands the Kafka
// integration.
//
// GET and HEAD requests are exempt — they SHOULD be idempotent and the
// browser fetch model + cookie SameSite=Lax already provide defense in
// depth. The middleware enforces ONLY the state-changing verbs.

package middleware

import (
	"net/http"
	"net/url"
	"strings"
)

// CSRFConfig configures the allowlist + enforcement scope.
type CSRFConfig struct {
	// AllowedOrigins is the exact origin allowlist. Each entry is matched
	// either by exact equality or, if the entry starts with ".", as a
	// subdomain-match suffix. E.g. ".he-api.com" matches
	// https://console.he-api.com AND https://he-api.com.
	//
	// Example for production:
	//   []string{".he-api.com"}
	//
	// Example for staging:
	//   []string{".staging.he-api.com"}
	//
	// Example for development:
	//   []string{"http://localhost:3000", "http://127.0.0.1:3000"}
	AllowedOrigins []string

	// DryRun = true logs violations + emits the upstream call WITHOUT
	// blocking. Used for staged rollouts; production should be false.
	DryRun bool
}

// CSRF returns the middleware. Apply BEFORE the route handlers so the
// upstream call is never made on rejection.
//
// Methods that bypass CSRF (GET / HEAD / OPTIONS) flow through
// unchanged. Methods that pass CSRF flow through. Methods that fail
// CSRF emit `403_csrf_check_failed` envelope (opaque) and return.
func CSRF(cfg CSRFConfig, next http.Handler) http.Handler {
	allowed := compileAllowlist(cfg.AllowedOrigins)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Safe methods are exempt — the GET/HEAD contract assumes
		// idempotency and the SameSite cookie policy already prevents
		// the cross-site case from carrying authentication.
		if isCSRFExempt(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header on a state-mutating request — same-origin
			// fetches from older browsers + curl invocations land here.
			// Treat as rejection (BR-4.6 strict mode) so a typo doesn't
			// silently bypass.
			if cfg.DryRun {
				next.ServeHTTP(w, r)
				return
			}
			writeCSRFViolation(w)
			return
		}

		if !originMatches(origin, allowed) {
			if cfg.DryRun {
				// Audit-only mode for rollout. Caller would log here.
				next.ServeHTTP(w, r)
				return
			}
			writeCSRFViolation(w)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isCSRFExempt returns true for HTTP methods that don't require Origin
// check. These are RFC-defined "safe" methods + OPTIONS (CORS preflight).
func isCSRFExempt(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// writeCSRFViolation emits the opaque 403 response per BR-4.6.
// Message is intentionally non-descriptive — attacker debugging this
// gets nothing useful.
func writeCSRFViolation(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	// Body is a fixed string — no detail, no per-request id leak.
	_, _ = w.Write([]byte(`{"error":{"code":"403_csrf_check_failed"}}`))
}

// allowEntry is a parsed allowlist entry. Either an exact-origin match
// (`http://localhost:3000`) or a domain-suffix match (`.he-api.com`).
type allowEntry struct {
	// exact, if non-empty, requires Origin to equal this string verbatim.
	exact string
	// suffix, if non-empty, matches Origin whose host equals the suffix
	// OR ends with `.` + suffix (subdomain match).
	suffix string
}

func compileAllowlist(raw []string) []allowEntry {
	out := make([]allowEntry, 0, len(raw))
	for _, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.HasPrefix(e, ".") {
			out = append(out, allowEntry{suffix: strings.TrimPrefix(e, ".")})
			continue
		}
		out = append(out, allowEntry{exact: e})
	}
	return out
}

// originMatches reports whether origin satisfies any entry in allowed.
// origin is the raw Origin header value, expected to be of the form
// `scheme://host[:port]` per RFC 6454.
func originMatches(origin string, allowed []allowEntry) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	host := parsed.Host
	// Strip port for suffix matching — the allowlist entries are
	// host-only domain suffixes (`.he-api.com`).
	hostNoPort := host
	if i := strings.IndexByte(host, ':'); i > 0 {
		hostNoPort = host[:i]
	}

	for _, a := range allowed {
		if a.exact != "" && a.exact == origin {
			return true
		}
		if a.suffix != "" {
			if hostNoPort == a.suffix {
				return true
			}
			if strings.HasSuffix(hostNoPort, "."+a.suffix) {
				return true
			}
		}
	}
	return false
}
