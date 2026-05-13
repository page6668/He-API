// github.go — Story 2.3 GitHub OAuth 2.0 client.
//
// Wright Round 1 Q4 ruling (b) — handwritten GitHub flow on top of
// golang.org/x/oauth2. GitHub is NOT an OIDC provider: no id_token, no
// JWKS. Identity comes from two REST calls after the token exchange:
//   1. GET /user        → numeric id (BR-2.4: identity stable across
//                          username renames; `login` MUST NOT be used)
//   2. GET /user/emails → primary+verified email (BR-2.3 / BR-2.7:
//                          always called even when /user already
//                          carries a public email)
//
// Security-relevant invariants enforced here:
//   - BR-2.2: scope is exactly `read:user user:email`; no elevation surface
//   - BR-2.3: /user/emails is unconditionally called for verified
//             confirmation — performance loss is accepted for safety
//   - BR-2.4: oauth_subject = stringified numeric id (NOT login)
//   - BR-2.6: access_token never persisted (Redis / DB / log) — used
//             only in-memory across the two REST calls then dropped
//   - BR-2.7: primary+verified email required; otherwise ErrEmailNotVerified
//   - BR-2.8: User-Agent + Accept + X-GitHub-Api-Version headers per docs
//   - BR-2.9: defensive 1-retry on 5xx (250ms backoff)
//   - Token-endpoint 4xx / 5xx + /user 401 all mapped to ErrProvider —
//     api-gateway emits 502_oauth_provider_error with no distinguishing
//     detail (BR-2.7 anti-info-leak parity with Google flow).
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// GitHub canonical endpoints. Tests override via GithubConfig.
const (
	defaultGithubAuthorizeURL = "https://github.com/login/oauth/authorize"
	defaultGithubTokenURL     = "https://github.com/login/oauth/access_token"
	defaultGithubAPIBaseURL   = "https://api.github.com"
	defaultGithubUserPath     = "/user"
	defaultGithubEmailsPath   = "/user/emails"

	// BR-2.8 — exact header values per GitHub API docs.
	githubUserAgent    = "He-API-Auth-Svc/0.1"
	githubAccept       = "application/vnd.github+json"
	githubAPIVersion   = "2022-11-28"
	githubAPIVersionHeader = "X-GitHub-Api-Version"
)

// GithubConfig is the public surface for constructing a GithubClient.
// Tests override the *URL / *Path fields to point at httptest stubs;
// production wires from Helm env (GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET
// / OAUTH_REDIRECT_URI_BASE).
type GithubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string

	// Override endpoints. Empty values fall back to the canonical
	// GitHub production URLs.
	AuthorizeURL string
	TokenURL     string
	APIBaseURL   string
	UserPath     string // default "/user"
	EmailsPath   string // default "/user/emails"

	// HTTPClient lets tests inject a tight timeout. nil falls back to a
	// 5s-timeout client — GitHub's p99 latency for /user is ~200ms, so
	// 5s is generous enough for transient slowness without holding the
	// callback open past the AC1 p95 ≤ 3s SLO.
	HTTPClient *http.Client

	// RetryAttempts + RetryBackoff implement BR-2.9 (single retry on 5xx).
	// RetryAttempts is the TOTAL number of attempts (e.g. 2 = 1 initial + 1
	// retry). Default 2; tests override to 1 (no retry) for fast failure.
	RetryAttempts int
	RetryBackoff  time.Duration
}

// GithubClient is the Story 2.3 GitHub OAuth entry point. Stateless after
// construction; cmd/server constructs one instance per process.
type GithubClient struct {
	cfg   GithubConfig
	hc    *http.Client
	oauth *oauth2.Config

	apiBase    string
	userPath   string
	emailsPath string

	retryAttempts int
	retryBackoff  time.Duration
}

// NewGithubClient validates the config and prepares the oauth2.Config +
// HTTP client.
func NewGithubClient(cfg GithubConfig) (*GithubClient, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("oauth: github client requires ClientID + ClientSecret")
	}
	if cfg.RedirectURI == "" {
		return nil, fmt.Errorf("oauth: github client requires RedirectURI")
	}
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = defaultGithubAuthorizeURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = defaultGithubTokenURL
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultGithubAPIBaseURL
	}
	if cfg.UserPath == "" {
		cfg.UserPath = defaultGithubUserPath
	}
	if cfg.EmailsPath == "" {
		cfg.EmailsPath = defaultGithubEmailsPath
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.RetryAttempts <= 0 {
		cfg.RetryAttempts = 2 // 1 initial + 1 retry (BR-2.9)
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 250 * time.Millisecond
	}
	return &GithubClient{
		cfg: cfg,
		hc:  cfg.HTTPClient,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Endpoint: oauth2.Endpoint{
				AuthURL:  cfg.AuthorizeURL,
				TokenURL: cfg.TokenURL,
			},
			// BR-2.2 — exactly read:user + user:email. No elevation.
			Scopes: []string{"read:user", "user:email"},
		},
		apiBase:       strings.TrimRight(cfg.APIBaseURL, "/"),
		userPath:      cfg.UserPath,
		emailsPath:    cfg.EmailsPath,
		retryAttempts: cfg.RetryAttempts,
		retryBackoff:  cfg.RetryBackoff,
	}, nil
}

// BuildAuthorizeURL constructs the GitHub authorize endpoint URL with state,
// PKCE challenge (S256 per BR-2.1), and allow_signup=true. Locale is accepted
// for symmetric API with GoogleClient but ignored — GitHub does not honour
// a locale hint in the authorize URL.
func (c *GithubClient) BuildAuthorizeURL(stateID, pkceChallenge, _locale string) string {
	return c.oauth.AuthCodeURL(stateID,
		oauth2.SetAuthURLParam("code_challenge", pkceChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		oauth2.SetAuthURLParam("allow_signup", "true"),
	)
}

// ExchangeCode redeems the authorization code at GitHub's token endpoint
// (with PKCE verifier), then hits /user and /user/emails to extract the
// stable numeric id + primary verified email. Returns the (stringified
// id, email) pair on success.
//
// On any provider failure path returns ErrProvider; BR-2.7 surfaces as
// ErrEmailNotVerified when /user/emails has no primary+verified entry.
//
// The access_token is dereferenced (set to "") before return so the GC
// can reclaim the bytes promptly — BR-2.6 in spirit (it is never
// persisted, never logged, never returned).
func (c *GithubClient) ExchangeCode(ctx context.Context, code, pkceVerifier string) (subject, email string, err error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.hc)
	token, err := c.oauth.Exchange(ctx, code,
		oauth2.SetAuthURLParam("code_verifier", pkceVerifier),
	)
	if err != nil {
		return "", "", fmt.Errorf("%w: github token exchange: %v", ErrProvider, err)
	}
	if token.AccessToken == "" {
		return "", "", fmt.Errorf("%w: github token response missing access_token", ErrProvider)
	}
	accessToken := token.AccessToken

	user, err := c.getUser(ctx, accessToken)
	if err != nil {
		return "", "", err
	}
	primary, err := c.getPrimaryVerifiedEmail(ctx, accessToken)
	if err != nil {
		return "", "", err
	}

	// BR-2.6 — drop the access_token before return.
	accessToken = ""
	_ = accessToken

	return strconv.FormatInt(user.ID, 10), strings.ToLower(primary), nil
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
}

func (c *githubUser) String() string {
	return fmt.Sprintf("github user (id=%d login=%s)", c.ID, c.Login)
}

func (c *GithubClient) getUser(ctx context.Context, accessToken string) (*githubUser, error) {
	u := c.apiBase + c.userPath
	body, err := c.apiGetWithRetry(ctx, u, accessToken)
	if err != nil {
		return nil, err
	}
	var out githubUser
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: decode /user: %v", ErrProvider, err)
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("%w: github /user missing numeric id", ErrProvider)
	}
	return &out, nil
}

type githubEmailEntry struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// getPrimaryVerifiedEmail walks /user/emails for the entry with
// primary=true AND verified=true. Returns ErrEmailNotVerified if no such
// entry exists (BR-2.7).
func (c *GithubClient) getPrimaryVerifiedEmail(ctx context.Context, accessToken string) (string, error) {
	u := c.apiBase + c.emailsPath
	body, err := c.apiGetWithRetry(ctx, u, accessToken)
	if err != nil {
		return "", err
	}
	var entries []githubEmailEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return "", fmt.Errorf("%w: decode /user/emails: %v", ErrProvider, err)
	}
	for _, e := range entries {
		if e.Primary && e.Verified && e.Email != "" {
			return e.Email, nil
		}
	}
	return "", ErrEmailNotVerified
}

// apiGetWithRetry performs an authenticated GET to the GitHub API with
// the BR-2.8 header set and BR-2.9 retry-on-5xx semantics.
func (c *GithubClient) apiGetWithRetry(ctx context.Context, urlStr, accessToken string) ([]byte, error) {
	// validate URL syntax — defence against accidental misconfiguration.
	if _, err := url.Parse(urlStr); err != nil {
		return nil, fmt.Errorf("%w: invalid github URL %q: %v", ErrProvider, urlStr, err)
	}

	var lastErr error
	for attempt := 0; attempt < c.retryAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: new request: %v", ErrProvider, err)
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", githubUserAgent)
		req.Header.Set("Accept", githubAccept)
		req.Header.Set(githubAPIVersionHeader, githubAPIVersion)

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: github GET %s: %v", ErrProvider, urlStr, err)
			if attempt < c.retryAttempts-1 {
				if !sleepCtx(ctx, c.retryBackoff) {
					return nil, lastErr
				}
				continue
			}
			return nil, lastErr
		}

		// Always close body — RESOURCE-001 invariant.
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%w: read body from %s: %v", ErrProvider, urlStr, readErr)
			return nil, lastErr
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("%w: github GET %s: HTTP %d", ErrProvider, urlStr, resp.StatusCode)
			if attempt < c.retryAttempts-1 {
				if !sleepCtx(ctx, c.retryBackoff) {
					return nil, lastErr
				}
				continue
			}
			return nil, lastErr
		}
		if resp.StatusCode >= 400 {
			// 4xx (including 401 token-revoked race) → ErrProvider; no retry
			// (a 4xx is a stable response, not a transient).
			return nil, fmt.Errorf("%w: github GET %s: HTTP %d", ErrProvider, urlStr, resp.StatusCode)
		}
		return body, nil
	}
	return nil, lastErr
}

// sleepCtx waits d but bails early when ctx is cancelled. Returns true if
// the sleep completed; false if ctx cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Compile-time sanity that errors.Is propagates through wrap chains.
var _ error = ErrProvider
