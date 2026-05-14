// Cookie attribute helpers per Story 2.2 BR-3.7 + TS-CONS-007.
//
// Two cookies are emitted by the gateway after a successful sign-in or
// refresh:
//
//   he_access   — JWT access token (15 min TTL).
//                 Path=/, SameSite=Lax, HttpOnly, Secure (in non-dev),
//                 Domain matches the deploy env.
//   he_refresh  — JWT refresh token (30 day TTL).
//                 Path=/v1/auth/refresh, SameSite=Strict, HttpOnly,
//                 Secure (in non-dev), Domain matches the deploy env.
//
// SameSite=Strict on the refresh cookie + narrow Path=/v1/auth/refresh
// means the refresh token is only ever transmitted to the refresh
// endpoint itself, not on any other navigation. The access cookie uses
// Lax so top-level navigation works while still blocking cross-site
// POSTs from carrying it (basic CSRF defense, complemented by the
// Origin check middleware in P5).
package handlers

import (
	"net/http"
	"strings"
)

// DeployEnv enumerates the three environments cookie domain / Secure
// gating distinguishes. Source: HE_API_DEPLOY_ENV at process start.
type DeployEnv string

const (
	EnvProduction  DeployEnv = "production"
	EnvStaging     DeployEnv = "staging"
	EnvDevelopment DeployEnv = "development"
)

// Cookie names per BR-3.7 + Story 2.4 BR-2.1.
const (
	AccessCookieName  = "he_access"
	RefreshCookieName = "he_refresh"
	// MFACookieName carries the short-lived (5-min) mfa_token between
	// password/OAuth-first-factor success and TOTP code submission.
	MFACookieName = "he_mfa"
)

// Cookie path scopes per BR-3.7 + Story 2.4.
const (
	accessCookiePath  = "/"
	refreshCookiePath = "/v1/auth/refresh"
	// Story 2.4 — narrow Path so the cookie is only sent to the 2FA
	// challenge surface. Defense against accidental cross-endpoint leakage.
	mfaCookiePath = "/v1/auth/2fa"

	// MFA cookie lifetime — must match the mfa_token JWT TTL (BR-2.1).
	mfaCookieMaxAgeSeconds = 5 * 60
)

// ParseDeployEnv normalizes the env-var value to a DeployEnv. Unknown
// values fall back to development so a misconfigured env doesn't
// accidentally drop Secure / Domain to production values.
func ParseDeployEnv(raw string) DeployEnv {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "production", "prod":
		return EnvProduction
	case "staging", "stage":
		return EnvStaging
	default:
		return EnvDevelopment
	}
}

// cookieDomain returns the Domain attribute for the env. Empty string
// means "omit the Domain attribute" — the browser will scope the cookie
// to the exact host that served it (correct for localhost dev).
func cookieDomain(env DeployEnv) string {
	switch env {
	case EnvProduction:
		return ".he-api.com"
	case EnvStaging:
		return ".staging.he-api.com"
	default:
		return ""
	}
}

// secureFlag returns true for prod + staging (HTTPS-only), false for dev.
func secureFlag(env DeployEnv) bool {
	return env != EnvDevelopment
}

// SetAccessCookie writes the canonical he_access Set-Cookie header.
// maxAgeSeconds typically tracks JWT access TTL (900 s = 15 min).
func SetAccessCookie(w http.ResponseWriter, value string, env DeployEnv, maxAgeSeconds int) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    value,
		Path:     accessCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   maxAgeSeconds,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// SetRefreshCookie writes the canonical he_refresh Set-Cookie header.
// SameSite=Strict + narrow Path means the refresh token only flows on
// explicit calls to /v1/auth/refresh.
func SetRefreshCookie(w http.ResponseWriter, value string, env DeployEnv, maxAgeSeconds int) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   maxAgeSeconds,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearAccessCookie writes an expired he_access Set-Cookie. Used on
// explicit signout (Story 2.5+) and defensively when refresh detects a
// compromised family.
func ClearAccessCookie(w http.ResponseWriter, env DeployEnv) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookieName,
		Value:    "",
		Path:     accessCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   -1,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearRefreshCookie writes an expired he_refresh Set-Cookie.
func ClearRefreshCookie(w http.ResponseWriter, env DeployEnv) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   -1,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// SetMFACookie writes the he_mfa cookie. The browser sends this cookie
// ONLY when the path begins with /v1/auth/2fa — exact scope per Architect
// §Rec.1. SameSite=Lax matches the access cookie since the 2FA challenge
// is a top-level navigation flow.
func SetMFACookie(w http.ResponseWriter, value string, env DeployEnv) {
	http.SetCookie(w, &http.Cookie{
		Name:     MFACookieName,
		Value:    value,
		Path:     mfaCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   mfaCookieMaxAgeSeconds,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearMFACookie writes an expired he_mfa Set-Cookie. Called on successful
// 2FA challenge (the JTI is also consumed server-side, so leaving the cookie
// would be benign — but clearing makes the client state explicit).
func ClearMFACookie(w http.ResponseWriter, env DeployEnv) {
	http.SetCookie(w, &http.Cookie{
		Name:     MFACookieName,
		Value:    "",
		Path:     mfaCookiePath,
		Domain:   cookieDomain(env),
		MaxAge:   -1,
		Secure:   secureFlag(env),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
