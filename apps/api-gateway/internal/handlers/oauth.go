// oauth.go — Story 2.3 api-gateway OAuth HTTP handlers.
//
// Wright Round 1 Q1 ruling: console BFF → api-gateway REST → auth-svc gRPC.
// The 4 endpoints below are the REST surface; each one re-proxies to one
// of the two AuthService.{Begin,Complete}OAuth gRPC RPCs and translates
// the response shape:
//
//   GET /v1/auth/oauth/google/initiate    →  302 to provider authorize URL
//                                            + Set-Cookie he_oauth_state
//   GET /v1/auth/oauth/google/callback    →  302 to return_to + Set-Cookie
//                                            he_access + he_refresh
//   GET /v1/auth/oauth/github/initiate    →  (same shape)
//   GET /v1/auth/oauth/github/callback    →  (same shape)
//
// The provider name is parameterized in the URL — internally a single
// initiate / callback function services both providers (just dispatches
// to the right OAuth client via auth-svc).
package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/he-api/he-api/packages/proto/gen/go/he/auth/v1"
)

// Cookie used to bind the OAuth callback to the initiate that issued it
// (BR-1.3). 10-minute Max-Age matches the Redis state TTL.
const (
	OAuthStateCookieName = "he_oauth_state"
	OAuthStateCookiePath = "/v1/auth/oauth"
	OAuthStateCookieTTL  = 600 // seconds
)

// OAuthHandler reverse-proxies the /v1/auth/oauth/* surface to auth-svc.
// Shares the Upstream client + Env with AuthProxy; tests construct it
// directly via the struct literal.
type OAuthHandler struct {
	Upstream interface {
		BeginOAuth(ctx context.Context, req *connect.Request[authv1.BeginOAuthRequest]) (*connect.Response[authv1.BeginOAuthResponse], error)
		CompleteOAuth(ctx context.Context, req *connect.Request[authv1.CompleteOAuthRequest]) (*connect.Response[authv1.CompleteOAuthResponse], error)
	}
	Env DeployEnv
}

// Initiate handles GET /v1/auth/oauth/{provider}/initiate.
//
// 1. Extract `provider` from the path (caller wires the mux pattern).
// 2. Validate `return_to` against the allow-list (BR-1.5) BEFORE any
//    Redis write — rejection is a 400 with no side effects.
// 3. Call auth-svc BeginOAuth.
// 4. Set he_oauth_state cookie with the returned state_id.
// 5. 302 to the provider authorize URL.
func (h *OAuthHandler) Initiate(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		returnTo := r.URL.Query().Get("return_to")
		if !IsAllowedReturnTo(returnTo, h.Env) {
			writeOAuthError(w, http.StatusBadRequest, "400_oauth_invalid_return_to")
			return
		}
		locale := r.URL.Query().Get("locale")
		if locale == "" {
			locale = "en"
		}
		clientIP := clientIPFromRequest(r)
		req := &authv1.BeginOAuthRequest{
			Provider:  provider,
			ReturnTo:  returnTo,
			Locale:    locale,
			ClientIp:  clientIP,
			UserAgent: r.Header.Get("User-Agent"),
		}
		resp, err := h.Upstream.BeginOAuth(r.Context(), connect.NewRequest(req))
		if err != nil {
			translateConnectError(w, r.Context(), err)
			return
		}
		setOAuthStateCookie(w, resp.Msg.GetStateId(), h.Env)
		http.Redirect(w, r, resp.Msg.GetAuthorizeUrl(), http.StatusFound)
	}
}

// Callback handles GET /v1/auth/oauth/{provider}/callback.
//
// 1. Verify `he_oauth_state` cookie present AND value == query.state.
//    Cookie mismatch → 400 anti-info-leak (don't redirect).
// 2. Clear the cookie (one-shot).
// 3. Call auth-svc CompleteOAuth with code + state_id + ip + ua.
// 4. On success: set he_access + he_refresh cookies, 302 to return_to.
// 5. On error: surface the auth-svc status string as a redirect to the
//    signin page with ?oauth_error= so the console toast can render
//    the i18n message.
func (h *OAuthHandler) Callback(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		stateID := q.Get("state")
		code := q.Get("code")
		// Provider may report user-cancelled consent via ?error=access_denied.
		if errParam := q.Get("error"); errParam != "" {
			redirectToSigninWithError(w, r, h.Env, "user_denied")
			return
		}
		if stateID == "" || code == "" {
			writeOAuthError(w, http.StatusBadRequest, "400_oauth_state_invalid")
			return
		}
		cookie, err := r.Cookie(OAuthStateCookieName)
		if err != nil || cookie.Value == "" || cookie.Value != stateID {
			// Cookie missing OR mismatch (BR-1.3). Both → state_invalid.
			clearOAuthStateCookie(w, h.Env)
			writeOAuthError(w, http.StatusBadRequest, "400_oauth_state_invalid")
			return
		}
		// One-shot — clear the cookie regardless of downstream outcome.
		clearOAuthStateCookie(w, h.Env)

		clientIP := clientIPFromRequest(r)
		req := &authv1.CompleteOAuthRequest{
			Provider:  provider,
			Code:      code,
			StateId:   stateID,
			ClientIp:  clientIP,
			UserAgent: r.Header.Get("User-Agent"),
		}
		resp, err := h.Upstream.CompleteOAuth(r.Context(), connect.NewRequest(req))
		if err != nil {
			// Translate the auth-svc status string into a console redirect
			// so the UI can render the toast. For unexpected errors fall
			// back to a generic toast key.
			oauthErr := oauthErrorCodeFromConnect(err)
			redirectToSigninWithError(w, r, h.Env, oauthErr)
			return
		}

		// Story 2.4 — auth-svc returns Requires_2Fa=true + mfa_token when
		// users.totp_enabled. Stamp the mfa_token into he_mfa cookie (narrow
		// Path=/v1/auth/2fa) so the browser only sends it back to the
		// challenge endpoint. 302 to /{locale}/2fa-challenge where the
		// console reads the cookie server-side and renders the TOTP form.
		if resp.Msg.GetRequires_2Fa() {
			if mfa := resp.Msg.GetMfaToken(); mfa != "" {
				SetMFACookie(w, mfa, h.Env)
			}
			locale := localeFromReturnTo(resp.Msg.GetReturnTo(), "en")
			target := buildAbsoluteConsoleURL(h.Env, "/"+locale+"/2fa-challenge")
			http.Redirect(w, r, target, http.StatusFound)
			return
		}

		// Issue access + refresh cookies (Story 2.2 helpers).
		SetAccessCookie(w, resp.Msg.GetAccessToken(), h.Env, accessCookieMaxAge())
		SetRefreshCookie(w, resp.Msg.GetRefreshToken(), h.Env, refreshCookieMaxAge())

		// 302 to return_to (already validated server-side; trust the auth-svc
		// echo because state.return_to was validated at initiate time).
		target := resp.Msg.GetReturnTo()
		if target == "" || !IsAllowedReturnTo(target, h.Env) {
			target = BuildDefaultReturnTo(localeFromReturnTo(target, "en"), h.Env)
		}
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// setOAuthStateCookie writes the binding cookie. Path is scoped to the
// OAuth surface so it never leaks to other endpoints.
func setOAuthStateCookie(w http.ResponseWriter, stateID string, env DeployEnv) {
	secure := env != EnvDevelopment
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthStateCookieName,
		Value:    stateID,
		Path:     OAuthStateCookiePath,
		MaxAge:   OAuthStateCookieTTL,
		Secure:   secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthStateCookie(w http.ResponseWriter, env DeployEnv) {
	secure := env != EnvDevelopment
	http.SetCookie(w, &http.Cookie{
		Name:     OAuthStateCookieName,
		Value:    "",
		Path:     OAuthStateCookiePath,
		MaxAge:   -1,
		Secure:   secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func accessCookieMaxAge() int  { return int((15 * time.Minute).Seconds()) }
func refreshCookieMaxAge() int { return int((7 * 24 * time.Hour).Seconds()) }

// clientIPFromRequest mirrors the convention Story 2.2 uses for signin:
// X-Forwarded-For first hop, falling back to RemoteAddr.
func clientIPFromRequest(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-For"); h != "" {
		if i := strings.Index(h, ","); i >= 0 {
			return strings.TrimSpace(h[:i])
		}
		return strings.TrimSpace(h)
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i >= 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

// writeOAuthError emits a small JSON error envelope. api-gateway's
// existing pattern uses this for all 4xx + 5xx responses.
func writeOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `"}}`))
}

// oauthErrorCodeFromConnect extracts a short error keyword for the
// /signin?oauth_error=… redirect param. The console toast looks up the
// matching i18n key (`auth.oauth.errors.{keyword}`).
func oauthErrorCodeFromConnect(err error) string {
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		return "provider_error"
	}
	code := cerr.Message()
	switch {
	case strings.Contains(code, "state_invalid"), strings.Contains(code, "state_expired"):
		return "state_invalid"
	case strings.Contains(code, "email_not_verified"):
		return "email_not_verified"
	case strings.Contains(code, "id_token_invalid"):
		return "provider_error"
	case strings.Contains(code, "link_unverified"):
		return "link_unverified"
	case strings.Contains(code, "subject_mismatch"):
		return "subject_mismatch"
	case strings.Contains(code, "cross_provider"), strings.Contains(code, "conflict_other_provider"):
		return "cross_provider"
	case strings.Contains(code, "provider_error"):
		return "provider_error"
	case strings.Contains(code, "invalid_credentials"):
		return "invalid_credentials"
	case strings.Contains(code, "account_suspended"):
		return "account_suspended"
	default:
		return "provider_error"
	}
}

// redirectToSigninWithError sends the user back to the console signin
// page with ?oauth_error=… so the toast renders.
func redirectToSigninWithError(w http.ResponseWriter, r *http.Request, env DeployEnv, errorKeyword string) {
	locale := localeFromCookie(r, "en")
	target := buildAbsoluteConsoleURL(env, "/"+locale+"/signin")
	if u, err := url.Parse(target); err == nil {
		q := u.Query()
		q.Set("oauth_error", errorKeyword)
		u.RawQuery = q.Encode()
		target = u.String()
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// buildAbsoluteConsoleURL prepends the env-specific console origin to the
// supplied path. Matches BuildDefaultReturnTo's host table.
func buildAbsoluteConsoleURL(env DeployEnv, path string) string {
	switch env {
	case EnvProduction:
		return "https://console.he-api.com" + path
	case EnvStaging:
		return "https://staging.console.he-api.com" + path
	default:
		return "http://localhost:3000" + path
	}
}

// localeFromReturnTo extracts the locale from a path or absolute URL.
// Falls back to `dflt` when extraction fails.
func localeFromReturnTo(returnTo, dflt string) string {
	if returnTo == "" {
		return dflt
	}
	// Absolute URL: parse + use path.
	u, err := url.Parse(returnTo)
	if err == nil {
		path := u.Path
		if !strings.HasPrefix(path, "/") {
			return dflt
		}
		rest := strings.TrimPrefix(path, "/")
		if slash := strings.Index(rest, "/"); slash > 0 {
			if supportedLocales[rest[:slash]] {
				return rest[:slash]
			}
		} else if supportedLocales[rest] {
			return rest
		}
	}
	return dflt
}

// localeFromCookie inspects the next-intl cookie or falls back.
// The console writes NEXT_LOCALE cookie on language switch.
func localeFromCookie(r *http.Request, dflt string) string {
	if c, err := r.Cookie("NEXT_LOCALE"); err == nil && supportedLocales[c.Value] {
		return c.Value
	}
	return dflt
}
