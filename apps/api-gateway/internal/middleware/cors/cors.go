// Package cors — Story 4.7 OQ-4.7-7 ratified CORS policy for `/public/*`.
//
// HARD CONSTRAINTS (Architect Round 1 ruling + m-1 anti-credential guard):
//
//  1. `/public/*` paths receive `Access-Control-Allow-Origin: *` and
//     `Access-Control-Allow-Methods: GET, OPTIONS`.
//  2. The wildcard MUST NEVER pair with `Access-Control-Allow-Credentials: true`
//     (standard CORS attack vector — the wildcard is rejected by the
//     browser when credentials is true, but middleware regressions can
//     re-introduce the combination; we never emit the credentials header).
//  3. Non-`/public/*` paths flow through UNCHANGED — the existing
//     CSRF Origin allowlist (Story-2.2 cascade) continues to gate
//     state-mutating verbs on bearer-protected + console routes.
//  4. Preflight `OPTIONS /public/*` returns 204 with the CORS headers and
//     SHORT-CIRCUITS — the inner handler never runs.
package cors

import (
	"net/http"
	"strings"
)

// PublicPathPrefix is the path namespace governed by this middleware.
// Exported so tests + mux wiring stay in sync without string drift.
const PublicPathPrefix = "/public/"

// PublicOriginWildcard is the value emitted on the
// `Access-Control-Allow-Origin` header for `/public/*` responses.
const PublicOriginWildcard = "*"

// PublicAllowedMethods is the value emitted on the
// `Access-Control-Allow-Methods` header for `/public/*` preflights.
const PublicAllowedMethods = "GET, OPTIONS"

// PublicCORS wraps next with the OQ-4.7-7 CORS policy. Apply at the same
// chain depth as SecurityHeaders / CSRF in main.go so every `/public/*`
// response carries the CORS headers and the OPTIONS preflight never
// reaches the auth-gated downstream.
//
// Non-`/public/*` requests pass through untouched.
func PublicCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, PublicPathPrefix) {
			next.ServeHTTP(w, r)
			return
		}

		// Always emit the wildcard on `/public/*` responses. We DO NOT echo
		// the request Origin (precedent: HuggingFace model-card API uses
		// `*` literally; per Architect m-1 the wildcard is the safe form
		// PROVIDED the credentials header is never set — see invariant
		// below). `Vary: Origin` is omitted intentionally for the same
		// reason (no per-origin response divergence).
		w.Header().Set("Access-Control-Allow-Origin", PublicOriginWildcard)
		w.Header().Set("Access-Control-Allow-Methods", PublicAllowedMethods)

		// INVARIANT (Architect m-1): we NEVER set
		// `Access-Control-Allow-Credentials` on `/public/*`. The browser
		// rejects `Access-Control-Allow-Origin: *` paired with credentials
		// true; emitting the header at all is a regression marker.

		if r.Method == http.MethodOptions {
			// Echo the requested headers if the client asked for them — typical
			// preflight from a third-party benchmark site sends a no-header
			// request, but capability-comparison sites that pre-fetch JSON
			// may attach Accept / Content-Type.
			if req := r.Header.Get("Access-Control-Request-Headers"); req != "" {
				w.Header().Set("Access-Control-Allow-Headers", req)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
