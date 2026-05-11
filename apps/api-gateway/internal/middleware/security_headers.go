// Package middleware holds the api-gateway HTTP middleware stack.
//
// Story 2.2 introduces three layers (P5 / T4):
//
//   - SecurityHeaders — emits HSTS / X-Content-Type-Options / X-Frame-Options /
//     Referrer-Policy / Content-Security-Policy on every response (BR-4.7).
//     ACTIVATED in P1 with the literal header set so 2.2-UNIT-190..194 regex
//     guards pass against the static source; values are finalized in P5.
//
//   - CSRF — Origin / Referer allowlist for state-mutating POSTs (BR-4.6).
//     P1 holds the stub interface only; activation lands in P5.
//
//   - JWTVerify — verifies access-token cookie on protected routes
//     (Story 2.5+ surface). P1 holds the stub only.
package middleware

import "net/http"

// SecurityHeaders writes the five mandatory security headers (BR-4.7,
// 2.2-UNIT-190..194). Values match docs/architecture/security.md baselines.
// CSP starts in report-only / permissive form here and tightens in Epic 3+.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'")
		next.ServeHTTP(w, r)
	})
}
