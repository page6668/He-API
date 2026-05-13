// oauth_redirect.go — Story 2.3 BR-1.5 return_to allow-list.
//
// Open-redirect defense: the OAuth flow accepts a caller-supplied
// `return_to` query parameter. If we 302 to whatever the user passed,
// an attacker can craft a URL like
// `/v1/auth/oauth/google/initiate?return_to=https://attacker.com` and
// turn the legitimate auth flow into a phishing redirect.
//
// IsAllowedReturnTo enforces a strict allow-list — env-aware. The matrix:
//
//   Production  → console.he-api.com only
//   Staging     → staging.console.he-api.com + console.he-api.com
//   Development → localhost:* + 127.0.0.1:* + relative paths
//
// In all envs, relative paths with a locale prefix (`/{locale}/...`)
// are allowed; api-gateway prepends the env-specific console origin.
//
// The classic attacker tricks all reject:
//   - https://attacker.com                                → external host
//   - https://console.he-api.com.attacker.com             → subdomain trick
//   - https://console.he-api.com@attacker.com             → @-trick (userinfo)
//   - https://console.he-api.com//attacker.com            → path confusion
//   - javascript:alert(1)                                 → scheme
//   - //attacker.com                                      → scheme-relative
//   - /dashboard (no locale)                              → bare-path fallback
package handlers

import (
	"net/url"
	"strings"
)

// allowedReturnHosts returns the host allow-list for the supplied env.
// Empty string fallbacks to development.
func allowedReturnHosts(env DeployEnv) []string {
	switch env {
	case EnvProduction:
		return []string{"console.he-api.com"}
	case EnvStaging:
		return []string{"staging.console.he-api.com", "console.he-api.com"}
	default:
		return []string{"localhost", "127.0.0.1"}
	}
}

// supportedLocales mirrors apps/console/i18n/config.ts. Updated whenever
// a new locale lands. Keep in lockstep with the auth-svc validLocales.
var supportedLocales = map[string]bool{
	"en":    true,
	"zh-CN": true,
	"ja":    true,
	"ko":    true,
	"es":    true,
	"fr":    true,
	"de":    true,
	"pt":    true,
	"ru":    true,
	"ar":    true,
}

// IsAllowedReturnTo returns true iff `returnTo` is safe to issue a 302
// to in the supplied environment. Empty input is allowed (caller falls
// back to `/{locale}/dashboard`).
//
// The function intentionally rejects more than the spec strictly requires
// — for instance, fragment-only URLs are rejected because they bypass the
// host check entirely on some platforms. When in doubt, reject.
func IsAllowedReturnTo(returnTo string, env DeployEnv) bool {
	if returnTo == "" {
		return true // empty → caller default
	}
	// Reject suspicious characters that can confuse the parser. Newlines,
	// control bytes, and the at-sign trick all fall here. The url.Parse
	// path catches most of these but defense-in-depth is cheap.
	if strings.ContainsAny(returnTo, "\r\n\t \x00") {
		return false
	}
	// Reject scheme-relative URLs explicitly — net/url.Parse treats
	// `//attacker.com/x` as a valid relative-reference with Host populated,
	// which is exactly the attacker payload.
	if strings.HasPrefix(returnTo, "//") {
		return false
	}
	// Reject obvious scheme abuse (javascript:, data:, etc.) before parsing
	// — net/url permits any scheme via the opaque/abs forms.
	if i := strings.Index(returnTo, ":"); i > 0 {
		scheme := strings.ToLower(returnTo[:i])
		if scheme != "https" && scheme != "http" {
			// Reject all non-http(s) schemes. http allowed only in dev (gated below).
			return false
		}
	}
	u, err := url.Parse(returnTo)
	if err != nil {
		return false
	}
	// Relative path: must start with `/{locale}/` where locale is supported.
	if u.Scheme == "" && u.Host == "" {
		return isAllowedRelativePath(u.Path)
	}
	// Absolute URL — verify scheme.
	if u.Scheme != "https" {
		// http only allowed in dev (e.g. http://localhost:3000/...)
		if env != EnvDevelopment {
			return false
		}
		if u.Scheme != "http" {
			return false
		}
	}
	// Userinfo present → @-trick → reject.
	if u.User != nil {
		return false
	}
	// Empty host after parse → reject defensively.
	if u.Host == "" {
		return false
	}
	// Host comparison must be exact against the allow-list (case-insensitive).
	host := strings.ToLower(u.Hostname())
	for _, allowed := range allowedReturnHosts(env) {
		if host == allowed {
			// Path may contain the //attacker.com path-confusion trick — reject
			// when the path begins with `//` because some browsers will
			// interpret a `//` path as scheme-relative on the next hop.
			if strings.HasPrefix(u.Path, "//") {
				return false
			}
			return true
		}
	}
	return false
}

// isAllowedRelativePath returns true iff `path` begins with `/{locale}/`
// for a supported locale. Bare paths like `/dashboard` are rejected so
// the api-gateway re-prefix step is deterministic (it doesn't have to
// guess the locale).
func isAllowedRelativePath(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	// Reject path-confusion `//`.
	if strings.HasPrefix(path, "//") {
		return false
	}
	rest := strings.TrimPrefix(path, "/")
	// First path segment must be a supported locale.
	slash := strings.Index(rest, "/")
	if slash == -1 {
		// `/en` with no trailing slash — accept iff locale is valid.
		return supportedLocales[rest]
	}
	locale := rest[:slash]
	return supportedLocales[locale]
}

// BuildDefaultReturnTo returns the env-appropriate fallback destination
// when the caller did not pass `return_to` (or it was rejected).
func BuildDefaultReturnTo(locale string, env DeployEnv) string {
	if !supportedLocales[locale] {
		locale = "en"
	}
	// Return absolute URL so the 302 Location always points at the console.
	switch env {
	case EnvProduction:
		return "https://console.he-api.com/" + locale + "/dashboard"
	case EnvStaging:
		return "https://staging.console.he-api.com/" + locale + "/dashboard"
	default:
		return "http://localhost:3000/" + locale + "/dashboard"
	}
}
