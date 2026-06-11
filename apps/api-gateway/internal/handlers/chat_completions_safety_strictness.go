// Story 8.4 — the per-request 内容安全严格度 resolver + carry (AC3).
//
// The level is resolved ONCE per request in ServeHTTP from the bearer
// CachedClaims (the Story-5.2 seam 8.2 reserved for 8.4), stored on the request
// context, and read back at the THREE filter decision points (input scan,
// non-stream redact, StreamGuard) so input and output always agree for a request
// (BR-3.5 resolve-once). The resolution FAILS CLOSED to Strict (block-all) when
// the claims are absent/empty/unknown — a 备案 gate never silently relaxes due to
// a cache-shape gap or a corrupt value (BR-3.4).
package handlers

import (
	"context"

	"github.com/he-api/he-api/apps/api-gateway/internal/contentsafety"
	"github.com/he-api/he-api/apps/api-gateway/internal/middleware"
)

// strictnessCtxKey carries the once-resolved per-request level.
type strictnessCtxKey struct{}

// withResolvedStrictness stores the per-request level on ctx (set once in
// ServeHTTP). The output paths read it back via resolvedStrictness so input and
// output never diverge for a single request.
func withResolvedStrictness(ctx context.Context, level contentsafety.Strictness) context.Context {
	return context.WithValue(ctx, strictnessCtxKey{}, level)
}

// resolveStrictnessFromClaims derives the level from the bearer CachedClaims via
// the Story-5.2 CacheValueFromContext seam. FAIL-CLOSED (BR-3.2/BR-3.4): nil/!ok
// claims → Strict; ParseStrictness maps ""/unknown/mixed-case → Strict too. This
// is the single resolution point (called once per request in ServeHTTP).
func resolveStrictnessFromClaims(ctx context.Context) contentsafety.Strictness {
	claims, ok := middleware.CacheValueFromContext(ctx)
	if !ok || claims == nil {
		return contentsafety.Strict // no bearer claims → block-all
	}
	return contentsafety.ParseStrictness(claims.ContentSafetyStrictness)
}

// resolvedStrictness returns the level stored by ServeHTTP. If the carry is
// absent (a handler path that did not set it, or a test), it re-resolves from the
// claims — still fail-closed to Strict. Reading the stored value (not re-parsing)
// is what makes the gate resolve-once: input + output read the SAME level.
func resolvedStrictness(ctx context.Context) contentsafety.Strictness {
	if level, ok := ctx.Value(strictnessCtxKey{}).(contentsafety.Strictness); ok {
		return level
	}
	return resolveStrictnessFromClaims(ctx)
}
