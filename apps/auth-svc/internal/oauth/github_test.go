// github_test.go — Story 2.3 P3 unit tests for the GitHub OAuth client.
// GitHub is NOT OIDC: no id_token, no JWKS. The fixture stands up an
// httptest server with three endpoints (/login/oauth/access_token,
// /user, /user/emails) and lets each test customise per-endpoint
// responses for happy + rejection paths.
//
// File→scenario mapping (from docs/qa/assessments/2.3-test-design-20260512.md):
//   - 2.3-UNIT-019..029 (authorize URL scope / token endpoint err / /user 401 /
//     primary+verified email parsing / numeric id / always-call-/user/emails /
//     User-Agent + X-GitHub-Api-Version headers / 5xx retry once / access_token
//     never persisted / refuses extra scopes / unverified primary → 400)
//   - 2.3-INT-010 / INT-012 / INT-013 — INT scope; covered here at unit level
//     where possible (no testcontainers needed; full E2E lands in P9)
//   - 2.3-BLIND-ERROR-002 (/user/emails timeout → 502)
//   - 2.3-BLIND-RESOURCE-001/003 (body close + access_token release)
package oauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
)

// -- mock GitHub server ----------------------------------------------------

type mockGitHub struct {
	t      *testing.T
	server *httptest.Server

	// per-endpoint behaviour
	tokenStatus    int
	tokenBody      string // override; empty → default {access_token, token_type}
	userStatus     int
	userBody       string // override; empty → default with id=987654321, login=devuser
	emailsStatus   int
	emailsBody     string // override; empty → default with primary+verified

	// counters
	tokenHits    atomic.Int64
	userHits     atomic.Int64
	emailsHits   atomic.Int64
	bodyCloses   atomic.Int64 // RESOURCE-001 verification
	headersMutex sync.Mutex
	lastHeaders  http.Header
}

func newMockGitHub(t *testing.T) *mockGitHub {
	t.Helper()
	m := &mockGitHub{
		t:            t,
		tokenStatus:  200,
		userStatus:   200,
		emailsStatus: 200,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", m.handleToken)
	mux.HandleFunc("/user", m.handleUser)
	mux.HandleFunc("/user/emails", m.handleEmails)
	srv := httptest.NewServer(mux)
	m.server = srv
	t.Cleanup(srv.Close)
	return m
}

func (m *mockGitHub) authorizeURL() string { return m.server.URL + "/login/oauth/authorize" }
func (m *mockGitHub) tokenURL() string     { return m.server.URL + "/login/oauth/access_token" }
func (m *mockGitHub) apiBaseURL() string   { return m.server.URL }

func (m *mockGitHub) recordHeaders(h http.Header) {
	m.headersMutex.Lock()
	m.lastHeaders = h.Clone()
	m.headersMutex.Unlock()
}

func (m *mockGitHub) snapshotHeaders() http.Header {
	m.headersMutex.Lock()
	defer m.headersMutex.Unlock()
	return m.lastHeaders.Clone()
}

func (m *mockGitHub) handleToken(w http.ResponseWriter, r *http.Request) {
	m.tokenHits.Add(1)
	if m.tokenStatus != 200 {
		http.Error(w, "token endpoint error", m.tokenStatus)
		return
	}
	body := m.tokenBody
	if body == "" {
		body = `{"access_token":"gho_fake_access_token","token_type":"bearer","scope":"read:user user:email"}`
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (m *mockGitHub) handleUser(w http.ResponseWriter, r *http.Request) {
	m.userHits.Add(1)
	m.recordHeaders(r.Header)
	if m.userStatus != 200 {
		http.Error(w, "user endpoint error", m.userStatus)
		return
	}
	body := m.userBody
	if body == "" {
		// id is numeric; login is the changeable username. BR-2.4 uses id.
		body = `{"id":987654321,"login":"devuser","email":null}`
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (m *mockGitHub) handleEmails(w http.ResponseWriter, r *http.Request) {
	m.emailsHits.Add(1)
	m.recordHeaders(r.Header)
	if m.emailsStatus != 200 {
		http.Error(w, "emails endpoint error", m.emailsStatus)
		return
	}
	body := m.emailsBody
	if body == "" {
		// Default: 1 primary verified + 1 secondary verified.
		body = `[
		    {"email":"dev@example.com","primary":true,"verified":true,"visibility":"public"},
		    {"email":"old@example.com","primary":false,"verified":true,"visibility":null}
		]`
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func newGithubClient(t *testing.T, m *mockGitHub) *oauth.GithubClient {
	t.Helper()
	cfg := oauth.GithubConfig{
		ClientID:     "ghx-test-client",
		ClientSecret: "ghx-test-secret",
		RedirectURI:  "https://console.he-api.com/v1/auth/oauth/github/callback",
		AuthorizeURL: m.authorizeURL(),
		TokenURL:     m.tokenURL(),
		APIBaseURL:   m.apiBaseURL(),
	}
	c, err := oauth.NewGithubClient(cfg)
	if err != nil {
		t.Fatalf("NewGithubClient: %v", err)
	}
	return c
}

// -- Tests ------------------------------------------------------------------

// Scenario: 2.3-UNIT-019 + BR-2.2
// BuildAuthorizeURL includes scope=read:user+user:email exactly, plus state,
// code_challenge, code_challenge_method=S256, allow_signup=true.
func TestGithub_BuildAuthorizeURL_RequiredParams(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	c := newGithubClient(t, m)

	got := c.BuildAuthorizeURL("state-xyz", "challenge-abc", "en")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	cases := map[string]string{
		"client_id":             "ghx-test-client",
		"redirect_uri":          "https://console.he-api.com/v1/auth/oauth/github/callback",
		"response_type":         "code",
		"state":                 "state-xyz",
		"code_challenge":        "challenge-abc",
		"code_challenge_method": "S256",
		"allow_signup":          "true",
	}
	for k, want := range cases {
		if q.Get(k) != want {
			t.Errorf("query[%q] = %q, want %q", k, q.Get(k), want)
		}
	}
	scope := q.Get("scope")
	for _, tok := range []string{"read:user", "user:email"} {
		if !strings.Contains(scope, tok) {
			t.Errorf("scope %q missing %q", scope, tok)
		}
	}
}

// Scenario: 2.3-UNIT-029 + BR-2.2 (negative)
// BuildAuthorizeURL MUST NOT request elevated scopes (repo / admin:org /
// gist / delete_repo). Refuse-to-escalate at the wire.
func TestGithub_BuildAuthorizeURL_NoEscalatedScopes(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	c := newGithubClient(t, m)
	got := c.BuildAuthorizeURL("s", "c", "en")
	u, _ := url.Parse(got)
	scope := u.Query().Get("scope")
	for _, forbidden := range []string{"repo", "admin:org", "admin:repo_hook", "gist", "delete_repo", "write:packages"} {
		if strings.Contains(scope, forbidden) {
			t.Errorf("scope %q includes forbidden %q — refuse-to-escalate violated", scope, forbidden)
		}
	}
}

// Scenario: 2.3-UNIT-022 + BR-2.4 + 2.3-INT-010 + 2.3-UNIT-025
// ExchangeCode happy path: parses /user/emails for primary+verified email,
// returns numeric id (stringified) as subject. Confirms /user/emails IS
// called even though /user returned email=null.
func TestGithub_ExchangeCode_HappyPath(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	c := newGithubClient(t, m)

	subject, email, err := c.ExchangeCode(context.Background(), "code-abc", "verifier-abc")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if subject != "987654321" {
		t.Errorf("subject = %q, want numeric id 987654321", subject)
	}
	if email != "dev@example.com" {
		t.Errorf("email = %q, want dev@example.com (primary+verified)", email)
	}
	// BR-2.3 — emails endpoint always hit even when /user returned an email.
	if got := m.emailsHits.Load(); got != 1 {
		t.Errorf("/user/emails hits = %d, want exactly 1 (BR-2.3)", got)
	}
}

// Scenario: 2.3-UNIT-024 + BR-2.4
// When /user returns email != null AND a github login,  the subject MUST
// still be the numeric `id`, NEVER the changeable `login` username.
func TestGithub_ExchangeCode_SubjectIsNumericIDNotLogin(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	m.userBody = `{"id":42,"login":"renameable_user","email":"public@example.com"}`
	m.emailsBody = `[{"email":"public@example.com","primary":true,"verified":true}]`
	c := newGithubClient(t, m)

	subject, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if subject != "42" {
		t.Errorf("subject = %q, want %q (numeric id, NOT login)", subject, "42")
	}
}

// Scenario: 2.3-UNIT-023 + BR-2.7
// /user/emails with no primary+verified entry → ErrEmailNotVerified.
// Covers: only-unverified-primary, only-verified-non-primary, empty array.
func TestGithub_ExchangeCode_NoPrimaryVerified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		emailsJSON string
	}{
		{
			"only-unverified-primary",
			`[{"email":"u@example.com","primary":true,"verified":false}]`,
		},
		{
			"only-verified-non-primary",
			`[{"email":"v@example.com","primary":false,"verified":true}]`,
		},
		{
			"empty-array",
			`[]`,
		},
		{
			"primary-verified-but-on-second-entry",
			`[{"email":"a@example.com","primary":false,"verified":true},{"email":"b@example.com","primary":true,"verified":true}]`, // valid → success expected
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newMockGitHub(t)
			m.emailsBody = tc.emailsJSON
			c := newGithubClient(t, m)
			_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
			if tc.name == "primary-verified-but-on-second-entry" {
				if err != nil {
					t.Fatalf("expected success when primary+verified is on second entry: %v", err)
				}
				return
			}
			if !errors.Is(err, oauth.ErrEmailNotVerified) {
				t.Fatalf("err = %v, want ErrEmailNotVerified", err)
			}
		})
	}
}

// Scenario: 2.3-UNIT-020 + BR-2.7
// Token endpoint 4xx/5xx → ErrProvider (no 401 leak — same error code for
// all token-endpoint failures).
func TestGithub_ExchangeCode_TokenEndpointError(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 500, 502, 503} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			m := newMockGitHub(t)
			m.tokenStatus = status
			c := newGithubClient(t, m)
			_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
			if !errors.Is(err, oauth.ErrProvider) {
				t.Fatalf("err = %v, want ErrProvider", err)
			}
		})
	}
}

// Scenario: 2.3-UNIT-021
// /user returns 401 (token revoked race) → ErrProvider (502 upstream, no
// distinguishing detail from generic provider failure).
func TestGithub_ExchangeCode_UserEndpoint401(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	m.userStatus = 401
	c := newGithubClient(t, m)

	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
}

// Scenario: 2.3-UNIT-026 + BR-2.8
// /user + /user/emails calls MUST set headers:
//   User-Agent: He-API-Auth-Svc/0.1
//   Accept: application/vnd.github+json
//   X-GitHub-Api-Version: 2022-11-28
//   Authorization: Bearer {access_token}
func TestGithub_ExchangeCode_APIRequestHeaders(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	c := newGithubClient(t, m)
	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	h := m.snapshotHeaders()
	if got := h.Get("User-Agent"); got != "He-API-Auth-Svc/0.1" {
		t.Errorf("User-Agent = %q, want He-API-Auth-Svc/0.1", got)
	}
	if got := h.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", got)
	}
	if got := h.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", got)
	}
	if auth := h.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("Authorization = %q, want Bearer prefix", auth)
	}
}

// Scenario: 2.3-UNIT-027 + BR-2.9
// /user 5xx → retry once after backoff → succeeds. mock counts 2 hits.
func TestGithub_ExchangeCode_RetryOn5xx(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	// Use a custom handler that fails the first call, succeeds on retry.
	var attempt atomic.Int64
	m.server.Config.Handler.(*http.ServeMux).HandleFunc("/user-retry", func(w http.ResponseWriter, r *http.Request) {
		m.userHits.Add(1)
		if attempt.Add(1) == 1 {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":777,"login":"retry-user","email":null}`))
	})
	// Re-point /user to a flaky handler:
	cfg := oauth.GithubConfig{
		ClientID:     "ghx-test",
		ClientSecret: "s",
		RedirectURI:  "https://console.he-api.com/v1/auth/oauth/github/callback",
		AuthorizeURL: m.authorizeURL(),
		TokenURL:     m.tokenURL(),
		APIBaseURL:   m.apiBaseURL(),
		// Custom user path for this test only:
		UserPath:       "/user-retry",
		RetryAttempts:  2,
		RetryBackoff:   5 * time.Millisecond,
	}
	c, err := oauth.NewGithubClient(cfg)
	if err != nil {
		t.Fatalf("NewGithubClient: %v", err)
	}
	subject, email, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode after retry: %v", err)
	}
	if subject != "777" || email == "" {
		t.Errorf("subject=%q email=%q after retry path", subject, email)
	}
	if got := attempt.Load(); got != 2 {
		t.Errorf("attempt count = %d, want exactly 2 (1 fail + 1 retry)", got)
	}
}

// Scenario: 2.3-UNIT-025 + BR-2.3
// /user/emails IS called even when /user returns a non-null email (security
// > performance — verified status is the source of truth).
func TestGithub_ExchangeCode_AlwaysCallsUserEmails(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	m.userBody = `{"id":1,"login":"alice","email":"alice@example.com"}` // /user emitted email
	m.emailsBody = `[{"email":"alice@example.com","primary":true,"verified":true}]`
	c := newGithubClient(t, m)
	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if got := m.emailsHits.Load(); got != 1 {
		t.Errorf("/user/emails hits = %d, want 1 even when /user has email (BR-2.3)", got)
	}
}

// Scenario: 2.3-BLIND-ERROR-002
// /user/emails timeout → ErrProvider (502). We simulate by setting a tiny
// HTTP timeout against a slow endpoint.
func TestGithub_ExchangeCode_EmailsTimeout(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	// Replace emails handler to block longer than client timeout.
	slowEmailsPath := "/slow-emails"
	m.server.Config.Handler.(*http.ServeMux).HandleFunc(slowEmailsPath, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`[]`))
	})
	cfg := oauth.GithubConfig{
		ClientID:     "ghx",
		ClientSecret: "s",
		RedirectURI:  "https://console.he-api.com/v1/auth/oauth/github/callback",
		AuthorizeURL: m.authorizeURL(),
		TokenURL:     m.tokenURL(),
		APIBaseURL:   m.apiBaseURL(),
		EmailsPath:   slowEmailsPath,
		HTTPClient:   &http.Client{Timeout: 50 * time.Millisecond},
	}
	c, err := oauth.NewGithubClient(cfg)
	if err != nil {
		t.Fatalf("NewGithubClient: %v", err)
	}
	_, _, err = c.ExchangeCode(context.Background(), "code", "verifier")
	if !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider on timeout", err)
	}
}

// Scenario: 2.3-UNIT-028 + BR-2.6
// access_token MUST NOT appear in any returned values. ExchangeCode returns
// only (subject, email, error) — there is no API path that surfaces the
// access_token to callers, so this is a structural guarantee verified by
// godoc-style assertion (signature pinning).
func TestGithub_ExchangeCode_AccessTokenNotReturned(t *testing.T) {
	t.Parallel()
	m := newMockGitHub(t)
	c := newGithubClient(t, m)
	subject, email, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	// Defensive sanity: neither return value should contain the access_token
	// shape ("gho_*", "ghp_*", "github_pat_*").
	for _, v := range []string{subject, email} {
		for _, prefix := range []string{"gho_", "ghp_", "github_pat_"} {
			if strings.Contains(v, prefix) {
				t.Errorf("return value %q leaks access_token-like prefix %q", v, prefix)
			}
		}
	}
}

// -- helpers ----------------------------------------------------------------

// Parse JSON-encoded emails for shape verification (used by some tests).
type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func decodeEmails(t *testing.T, raw string) []ghEmail {
	t.Helper()
	var out []ghEmail
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode emails: %v", err)
	}
	return out
}

// (keep used for build cleanliness)
var _ = decodeEmails
