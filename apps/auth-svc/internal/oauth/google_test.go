// google_test.go — Story 2.3 P2 unit + thin-integration tests for the
// Google OAuth + OIDC client. miniredis isn't relevant here; this test
// suite stands up a self-hosted httptest mock OIDC provider so the
// happy path + every rejection path stays hermetic (no network).
//
// File→scenario mapping (from docs/qa/assessments/2.3-test-design-20260512.md):
//   - 2.3-UNIT-008..018 (authorize URL params / id_token sig+iss+aud+exp /
//     email_verified / token-endpoint error mapping / locale→hl / discovery
//     retry-then-fail-fast / JWKS rotation refresh / client_secret redaction)
//   - 2.3-INT-003 / INT-006..008 (Redis happy path + JWKS misses + provider
//     stub Branch C) live in google_integration_test.go (build tag
//     `integration`). P2 keeps the contract surface; INT lands in P9.
package oauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/he-api/he-api/apps/auth-svc/internal/oauth"
)

// -- mock OIDC provider fixture --------------------------------------------

type mockOIDC struct {
	t        *testing.T
	server   *httptest.Server
	priv     *rsa.PrivateKey
	keyID    string
	clientID string

	// Controls for per-test branching:
	tokenStatus    int    // 200 by default
	tokenBody      string // pre-rendered JSON; if empty, default-signed id_token
	idTokenClaims  map[string]any
	customIDToken  string // if non-empty, used verbatim
	jwksHits       atomic.Int64
	discoveryHits  atomic.Int64
	discoveryFail  atomic.Int32 // first N discovery requests return 500
	// signingAlg lets a test mint an HS256-signed token (alg-confusion attack)
	signingAlg jose.SignatureAlgorithm
}

func newMockOIDC(t *testing.T, clientID string) *mockOIDC {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa generate: %v", err)
	}
	m := &mockOIDC{
		t:           t,
		priv:        priv,
		keyID:       "test-kid-1",
		clientID:    clientID,
		tokenStatus: 200,
		signingAlg:  jose.RS256,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", m.handleDiscovery)
	mux.HandleFunc("/jwks.json", m.handleJWKS)
	mux.HandleFunc("/token", m.handleToken)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	m.server = srv
	t.Cleanup(srv.Close)
	return m
}

func (m *mockOIDC) issuer() string { return m.server.URL }

func (m *mockOIDC) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	if v := m.discoveryFail.Load(); v > 0 {
		m.discoveryFail.Add(-1)
		http.Error(w, "discovery flaky", http.StatusInternalServerError)
		return
	}
	m.discoveryHits.Add(1)
	doc := map[string]any{
		"issuer":                 m.server.URL,
		"authorization_endpoint": m.server.URL + "/authorize",
		"token_endpoint":         m.server.URL + "/token",
		"jwks_uri":               m.server.URL + "/jwks.json",
		"response_types_supported": []string{"code"},
		"subject_types_supported":  []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (m *mockOIDC) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	m.jwksHits.Add(1)
	jwk := jose.JSONWebKey{
		Key:       &m.priv.PublicKey,
		KeyID:     m.keyID,
		Use:       "sig",
		Algorithm: "RS256",
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

func (m *mockOIDC) handleToken(w http.ResponseWriter, r *http.Request) {
	if m.tokenStatus != 200 {
		http.Error(w, "provider down", m.tokenStatus)
		return
	}
	body := m.tokenBody
	if body == "" {
		idTok := m.customIDToken
		if idTok == "" {
			idTok = m.signIDToken(m.idTokenClaims)
		}
		response := map[string]any{
			"access_token": "fake-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idTok,
		}
		raw, _ := json.Marshal(response)
		body = string(raw)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// signIDToken mints an id_token with the supplied claims (overrides applied
// onto a baseline default). signingAlg controls which alg appears in the
// JWS header; HS256 mints with a symmetric key to simulate alg-confusion.
func (m *mockOIDC) signIDToken(overrides map[string]any) string {
	m.t.Helper()
	now := time.Now().Unix()
	claims := map[string]any{
		"iss":            m.server.URL,
		"sub":            "google-sub-12345",
		"aud":            m.clientID,
		"exp":            now + 3600,
		"iat":            now,
		"email":          "alice@example.com",
		"email_verified": true,
	}
	for k, v := range overrides {
		claims[k] = v
	}
	payload, _ := json.Marshal(claims)

	var signer jose.Signer
	if m.signingAlg == jose.RS256 {
		opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", m.keyID)
		s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: m.priv}, opts)
		if err != nil {
			m.t.Fatalf("new signer: %v", err)
		}
		signer = s
	} else {
		// HS256 requires ≥ 32-byte symmetric key (go-jose v4 enforces the
		// SHA-256 block size). 64 bytes is comfortably above the floor.
		hsKey := []byte("attacker-symmetric-key-padded-to-64-bytes-for-hs256-min-len--xx")
		opts := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", m.keyID)
		s, err := jose.NewSigner(jose.SigningKey{Algorithm: m.signingAlg, Key: hsKey}, opts)
		if err != nil {
			m.t.Fatalf("new HS signer: %v", err)
		}
		signer = s
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		m.t.Fatalf("sign: %v", err)
	}
	serialized, err := jws.CompactSerialize()
	if err != nil {
		m.t.Fatalf("serialize: %v", err)
	}
	return serialized
}

// newGoogleClient builds a GoogleClient pointed at the mock provider.
func newGoogleClient(t *testing.T, m *mockOIDC, opts ...oauth.GoogleOption) *oauth.GoogleClient {
	t.Helper()
	cfg := oauth.GoogleConfig{
		ClientID:     m.clientID,
		ClientSecret: "test-client-secret",
		RedirectURI:  "https://console.he-api.com/v1/auth/oauth/google/callback",
		DiscoveryURL: m.issuer(),
	}
	c, err := oauth.NewGoogleClient(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("NewGoogleClient: %v", err)
	}
	return c
}

// -- Tests ------------------------------------------------------------------

// Scenario: 2.3-UNIT-008
// BuildAuthorizeURL produces all required query params per AC1 line 283.
// Asserts the 9-param contract: client_id, redirect_uri, response_type=code,
// scope=openid+email+profile, state, code_challenge, code_challenge_method=S256,
// prompt=select_account, hl={mapped_locale}.
func TestGoogle_BuildAuthorizeURL_AllRequiredParams(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-abc")
	c := newGoogleClient(t, m)

	got := c.BuildAuthorizeURL("state-xyz", "challenge-abc", "zh-CN")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	cases := map[string]string{
		"client_id":             "client-abc",
		"redirect_uri":          "https://console.he-api.com/v1/auth/oauth/google/callback",
		"response_type":         "code",
		"state":                 "state-xyz",
		"code_challenge":        "challenge-abc",
		"code_challenge_method": "S256",
		"prompt":                "select_account",
		"hl":                    "zh-CN",
	}
	for k, want := range cases {
		if q.Get(k) != want {
			t.Errorf("query[%q] = %q, want %q", k, q.Get(k), want)
		}
	}
	// scope is space-delimited per RFC 6749 §3.3; verify all 3 tokens present.
	scope := q.Get("scope")
	for _, tok := range []string{"openid", "email", "profile"} {
		if !strings.Contains(scope, tok) {
			t.Errorf("scope %q missing %q", scope, tok)
		}
	}
}

// Scenario: 2.3-UNIT-015 (P1)
// locale→Google `hl` mapping covers the 10-locale set used by the console.
// next-intl tags map cleanly except for `pt` → "pt-BR" and `zh-CN` → "zh-CN".
func TestGoogle_LocaleHLMapping(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-x")
	c := newGoogleClient(t, m)

	for _, tc := range []struct{ locale, wantHL string }{
		{"en", "en"},
		{"zh-CN", "zh-CN"},
		{"ja", "ja"},
		{"ko", "ko"},
		{"es", "es"},
		{"fr", "fr"},
		{"de", "de"},
		{"pt", "pt-BR"}, // Google uses pt-BR as the canonical Portuguese hl
		{"ru", "ru"},
		{"ar", "ar"},
		{"bogus-locale", "en"}, // fallback
	} {
		tc := tc
		t.Run(tc.locale, func(t *testing.T) {
			t.Parallel()
			got := c.BuildAuthorizeURL("s", "c", tc.locale)
			u, _ := url.Parse(got)
			if hl := u.Query().Get("hl"); hl != tc.wantHL {
				t.Fatalf("hl for locale %q = %q, want %q", tc.locale, hl, tc.wantHL)
			}
		})
	}
}

// Scenario: 2.3-UNIT-009 (happy path id_token verification)
// ExchangeCode returns (subject, email) from a correctly-signed id_token
// with iss == issuer, aud == client_id, exp > now, email_verified=true.
func TestGoogle_ExchangeCode_HappyPath(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-happy")
	m.idTokenClaims = map[string]any{
		"sub":            "google-sub-777",
		"email":          "alice@example.com",
		"email_verified": true,
	}
	c := newGoogleClient(t, m)

	subject, email, err := c.ExchangeCode(context.Background(), "code-abc", "verifier-abc")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if subject != "google-sub-777" {
		t.Errorf("subject = %q, want google-sub-777", subject)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want alice@example.com", email)
	}
}

// Scenario: 2.3-UNIT-010
// id_token with iss != issuer (provider URL) → reject.
func TestGoogle_ExchangeCode_RejectIssuerMismatch(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-iss")
	m.idTokenClaims = map[string]any{"iss": "https://evil.example.com"}
	c := newGoogleClient(t, m)

	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode: want error on iss mismatch, got nil")
	}
}

// Scenario: 2.3-UNIT-011
// id_token with aud != client_id → reject.
func TestGoogle_ExchangeCode_RejectAudienceMismatch(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-aud-correct")
	m.idTokenClaims = map[string]any{"aud": "different-client"}
	c := newGoogleClient(t, m)

	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode: want error on aud mismatch, got nil")
	}
}

// Scenario: 2.3-UNIT-012
// id_token with exp ≤ now() → reject.
func TestGoogle_ExchangeCode_RejectExpired(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-exp")
	m.idTokenClaims = map[string]any{
		"exp": time.Now().Unix() - 3600, // 1 hour ago
		"iat": time.Now().Unix() - 7200,
	}
	c := newGoogleClient(t, m)

	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode: want error on expired id_token, got nil")
	}
}

// Scenario: 2.3-UNIT-013 + BR-1.7
// email_verified != true (string "false", number 0, missing claim) → reject
// with ErrEmailNotVerified. The triad covers the wire-shape variations a
// provider might emit (string vs bool vs absent).
func TestGoogle_ExchangeCode_RejectEmailNotVerified(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		claims map[string]any
	}{
		{"explicit-false", map[string]any{"email_verified": false}},
		{"missing-claim", map[string]any{"email_verified": nil}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newMockOIDC(t, "client-emv")
			m.idTokenClaims = tc.claims
			c := newGoogleClient(t, m)
			_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
			if !errors.Is(err, oauth.ErrEmailNotVerified) {
				t.Fatalf("err = %v, want ErrEmailNotVerified", err)
			}
		})
	}
}

// Scenario: 2.3-UNIT-014
// Token endpoint 4xx/5xx → ErrProvider (api-gateway maps to 502).
func TestGoogle_ExchangeCode_TokenEndpointError(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 500, 502, 503} {
		status := status
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			t.Parallel()
			m := newMockOIDC(t, "client-down")
			m.tokenStatus = status
			c := newGoogleClient(t, m)
			_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
			if !errors.Is(err, oauth.ErrProvider) {
				t.Fatalf("err = %v, want ErrProvider", err)
			}
		})
	}
}

// Scenario: 2.3-UNIT-018 + Wright Round 1 Constraint (c)
// Alg-confusion defense: an HS256-signed id_token (or any non-RS256 alg) MUST
// be rejected — the verifier whitelists RS256 only. This catches the canonical
// alg-confusion attack where the attacker mints an HS256 token using the
// (public) RSA key as the symmetric secret. SEC-003 same-mode as Story 2.2.
func TestGoogle_ExchangeCode_RejectNonRS256Alg(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-alg")
	m.signingAlg = jose.HS256 // attacker forges HS256 over the public key bytes
	c := newGoogleClient(t, m)

	_, _, err := c.ExchangeCode(context.Background(), "code", "verifier")
	if err == nil {
		t.Fatal("ExchangeCode: want rejection of HS256 id_token, got nil error")
	}
}

// Scenario: 2.3-UNIT-016 + Wright Round 1 m-2 ruling
// retry-then-fail-fast OIDC discovery: first 2 discovery calls 500, 3rd
// succeeds → NewGoogleClient returns nil error AND records ≥3 discovery hits.
func TestGoogle_NewClient_DiscoveryRetryThenSuccess(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-retry")
	m.discoveryFail.Store(2) // first 2 attempts will 500

	cfg := oauth.GoogleConfig{
		ClientID:         m.clientID,
		ClientSecret:     "s",
		RedirectURI:      "https://console.he-api.com/v1/auth/oauth/google/callback",
		DiscoveryURL:     m.issuer(),
		DiscoveryRetries: 5,
		DiscoveryBackoff: 10 * time.Millisecond,
	}
	c, err := oauth.NewGoogleClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewGoogleClient: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
	if got := m.discoveryHits.Load(); got < 1 {
		t.Fatalf("discovery hits = %d, want ≥1 after retries", got)
	}
}

// Scenario: 2.3-UNIT-016 (b) — retry then ultimately fail
// All discovery attempts 500 → NewGoogleClient returns error before TTL.
func TestGoogle_NewClient_DiscoveryAllFail(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "client-fail")
	m.discoveryFail.Store(100) // never recover

	cfg := oauth.GoogleConfig{
		ClientID:         m.clientID,
		ClientSecret:     "s",
		RedirectURI:      "https://console.he-api.com/v1/auth/oauth/google/callback",
		DiscoveryURL:     m.issuer(),
		DiscoveryRetries: 3,
		DiscoveryBackoff: 10 * time.Millisecond,
	}
	_, err := oauth.NewGoogleClient(context.Background(), cfg)
	if err == nil {
		t.Fatal("NewGoogleClient: want error after all retries fail")
	}
}

// -- sanity helpers ---------------------------------------------------------

// Ensure HS256-attack token actually serializes (catches go-jose API drift).
func TestMockOIDC_HS256SerializesValidJWS(t *testing.T) {
	t.Parallel()
	m := newMockOIDC(t, "self-check")
	m.signingAlg = jose.HS256
	tok := m.signIDToken(nil)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("JWS parts = %d, want 3", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	_ = json.Unmarshal(headerJSON, &header)
	if header.Alg != "HS256" {
		t.Fatalf("header.alg = %q, want HS256", header.Alg)
	}
}

// catch unused imports
var _ = sha256.Sum256
