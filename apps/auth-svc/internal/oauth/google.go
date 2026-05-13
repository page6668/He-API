// google.go — Story 2.3 Google OAuth 2.0 + OIDC client.
//
// Wright Round 1 Q4 ruling (b): coreos/go-oidc/v3 for OIDC discovery +
// JWKS caching + id_token verification; golang.org/x/oauth2 for the
// Authorization Code exchange surface. The combined dependency footprint
// stays minimal (≤ 5 transitive direct deps) and avoids re-implementing
// JWKS rotation logic that Story 2.2 already paid for in the jwt pkg.
//
// Constraints baked in here:
//   - Constraint (b) (m-2 ruling): OIDC discovery uses retry-then-fail-fast
//     at startup (3 attempts default, exp backoff, ≤ 30s total). Pod
//     restart-loops during transient provider hiccups are avoided.
//   - Constraint (c): `oidc.Config.SupportedSigningAlgs = []string{"RS256"}`
//     pins the signature algorithm to RS256 — blocks the canonical
//     alg-confusion attack (HS256 signed with the RSA public key bytes
//     would otherwise verify against go-oidc's default any-alg policy).
//
// BR-1.7 surfaces as the package sentinel ErrEmailNotVerified — every
// Google id_token whose email_verified claim is anything other than
// boolean true is rejected at this layer. ErrProvider wraps token-endpoint
// 4xx/5xx + id_token-verification failures uniformly; api-gateway maps
// to 502_oauth_provider_error without leaking provider-internal state.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Sentinel errors specific to provider flows.
//
// ErrProvider is the umbrella for "the provider misbehaved" — token
// endpoint 4xx/5xx, network failure, id_token verification failure, or
// any other path where the caller must surface a 502 without leaking
// internal state. The audit publisher distinguishes the precise reason
// via the wrapped error chain (errors.Is + the underlying error string).
//
// ErrEmailNotVerified is BR-1.7 / BR-2.7 — the provider claims an email
// but did not mark it verified. Auth-svc must reject; api-gateway maps
// to 400_oauth_email_not_verified with an i18n hint to verify-via-password
// first.
var (
	ErrProvider         = errors.New("oauth: provider error")
	ErrEmailNotVerified = errors.New("oauth: provider email not verified")
	ErrIDTokenMissing   = errors.New("oauth: id_token absent from token response")
)

// GoogleConfig is the public surface for constructing a GoogleClient.
// Production wires the values from Helm env (GOOGLE_CLIENT_ID /
// GOOGLE_CLIENT_SECRET / OAUTH_REDIRECT_URI_BASE); tests inject a fake
// DiscoveryURL pointing at an httptest server.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string

	// RedirectURI is the absolute URL Google redirects to after consent.
	// The OAuth provider's authorized-redirect-URI list MUST include this
	// value exactly — Google rejects mismatches at the authorize step.
	RedirectURI string

	// DiscoveryURL defaults to "https://accounts.google.com" when empty.
	// Production sticks with the default; tests override to an httptest
	// server URL.
	DiscoveryURL string

	// HTTPClient lets tests inject a custom round-tripper. nil falls back
	// to a sensible default (15s overall timeout matching the AC1 latency
	// SLO p95 ≤ 3s × 5).
	HTTPClient *http.Client

	// DiscoveryRetries + DiscoveryBackoff implement the m-2 ruling:
	// retry-then-fail-fast. Defaults: 3 attempts × 2s base = ~14s total
	// when all attempts fail (2 + 4 + 8 = 14s). Tests override to small
	// values to keep the suite fast.
	DiscoveryRetries int
	DiscoveryBackoff time.Duration
	DiscoveryMaxBackoff time.Duration
}

// GoogleOption lets the cmd/server wiring layer (or tests) tweak ancillary
// behaviour without bloating the config struct. Today only HL mapping
// override is exposed; future options (e.g. custom claim extraction)
// follow the same functional-option pattern.
type GoogleOption func(*GoogleClient)

// WithLocaleHL overrides the locale-to-`hl` mapping. Production sticks
// with the default mapping baked into `defaultGoogleHL`.
func WithLocaleHL(fn func(locale string) string) GoogleOption {
	return func(c *GoogleClient) { c.hl = fn }
}

// GoogleClient is the Story 2.3 Google OAuth + OIDC entry point.
// Stateless after construction — the underlying oidc.Provider caches
// JWKS, the oauth2.Config is value-only. cmd/server constructs one
// instance per process; handlers receive the *GoogleClient via DI.
type GoogleClient struct {
	cfg      GoogleConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	hc       *http.Client
	hl       func(string) string
}

// NewGoogleClient performs OIDC discovery (with m-2 retry-then-fail-fast)
// and prepares the JWKS-backed verifier whitelisted to RS256.
func NewGoogleClient(ctx context.Context, cfg GoogleConfig, opts ...GoogleOption) (*GoogleClient, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("oauth: google client requires ClientID + ClientSecret")
	}
	if cfg.RedirectURI == "" {
		return nil, fmt.Errorf("oauth: google client requires RedirectURI")
	}
	if cfg.DiscoveryURL == "" {
		cfg.DiscoveryURL = "https://accounts.google.com"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.DiscoveryRetries <= 0 {
		cfg.DiscoveryRetries = 3
	}
	if cfg.DiscoveryBackoff <= 0 {
		cfg.DiscoveryBackoff = 2 * time.Second
	}
	if cfg.DiscoveryMaxBackoff <= 0 {
		cfg.DiscoveryMaxBackoff = 10 * time.Second
	}

	// m-2 ruling: retry-then-fail-fast.
	ctx = oidc.ClientContext(ctx, cfg.HTTPClient)
	var provider *oidc.Provider
	var lastErr error
	backoff := cfg.DiscoveryBackoff
	for attempt := 0; attempt < cfg.DiscoveryRetries; attempt++ {
		p, err := oidc.NewProvider(ctx, cfg.DiscoveryURL)
		if err == nil {
			provider = p
			break
		}
		lastErr = err
		if attempt == cfg.DiscoveryRetries-1 {
			break
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("oauth: discovery cancelled: %w", ctx.Err())
		}
		backoff *= 2
		if backoff > cfg.DiscoveryMaxBackoff {
			backoff = cfg.DiscoveryMaxBackoff
		}
	}
	if provider == nil {
		return nil, fmt.Errorf("oauth: google OIDC discovery failed after %d attempts: %w", cfg.DiscoveryRetries, lastErr)
	}

	c := &GoogleClient{
		cfg:      cfg,
		provider: provider,
		hc:       cfg.HTTPClient,
		hl:       defaultGoogleHL,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		// Constraint (c): pin RS256 — blocks alg-confusion attacks.
		verifier: provider.Verifier(&oidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: []string{"RS256"},
		}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// BuildAuthorizeURL constructs the provider's authorize endpoint URL with
// the 9 required query params per AC1 GIVEN/WHEN/THEN. State + PKCE
// challenge originate from StateService; locale is the user's next-intl
// tag (mapped to Google `hl` via the configured mapper).
//
// The redirect_uri + client_id + response_type=code + scope params come
// from oauth2.Config.AuthCodeURL; the remaining 5 (code_challenge,
// code_challenge_method, prompt, hl, state) are passed via SetAuthURLParam.
// state is the second arg to AuthCodeURL by convention.
func (c *GoogleClient) BuildAuthorizeURL(stateID, pkceChallenge, locale string) string {
	return c.oauth.AuthCodeURL(stateID,
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("prompt", "select_account"),
		oauth2.SetAuthURLParam("hl", c.hl(locale)),
	)
}

// ExchangeCode redeems the authorization code at the provider's token
// endpoint (with PKCE verifier), verifies the id_token signature + claims
// against the cached JWKS, and returns the (subject, email) pair on
// success. The provider's email_verified claim is enforced server-side
// (BR-1.7) — a callback whose id_token bears email_verified=false is
// rejected with ErrEmailNotVerified.
//
// Errors wrapped with ErrProvider on token-endpoint or verification
// failures; api-gateway maps to 502_oauth_provider_error. ErrEmailNotVerified
// maps to 400_oauth_email_not_verified.
func (c *GoogleClient) ExchangeCode(ctx context.Context, code, pkceVerifier string) (subject, email string, err error) {
	ctx = oidc.ClientContext(ctx, c.hc)
	token, err := c.oauth.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", pkceVerifier),
	)
	if err != nil {
		return "", "", fmt.Errorf("%w: token exchange: %v", ErrProvider, err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", "", ErrIDTokenMissing
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return "", "", fmt.Errorf("%w: id_token verify: %v", ErrProvider, err)
	}
	// Claims extraction. email_verified MUST be present + boolean true
	// (BR-1.7). The intermediate json.RawMessage lets us distinguish
	// missing claim ("absent") from explicit false ("present, false") for
	// audit precision, even though both map to ErrEmailNotVerified.
	var claims struct {
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", fmt.Errorf("%w: claims decode: %v", ErrProvider, err)
	}
	if !claimIsTrueBool(claims.EmailVerified) {
		return "", "", ErrEmailNotVerified
	}
	if claims.Email == "" {
		return "", "", fmt.Errorf("%w: id_token missing email claim", ErrProvider)
	}
	return idToken.Subject, strings.ToLower(claims.Email), nil
}

// claimIsTrueBool returns true iff the raw JSON value is exactly the
// literal `true`. String "true" / numeric 1 / missing claim all return
// false — Google's id_token spec emits boolean true on verified emails;
// any deviation is treated as not-verified for safety.
func claimIsTrueBool(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "true"
}

// defaultGoogleHL maps the console's 10-locale set to Google's `hl`
// language parameter. Google accepts BCP 47 tags but emits its UI strings
// from a smaller table — pt-BR is the canonical Portuguese tag (Google
// doesn't ship pt-PT separately). Unknown locales fall back to English.
func defaultGoogleHL(locale string) string {
	switch locale {
	case "en", "zh-CN", "ja", "ko", "es", "fr", "de", "ru", "ar":
		return locale
	case "pt":
		return "pt-BR"
	default:
		return "en"
	}
}
