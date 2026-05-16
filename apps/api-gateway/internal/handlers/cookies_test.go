// Story 2.5 — SetLocaleCookie attribute-parity tests (Architect Q3 ruling +
// BR-3.5). Asserts the cookie attributes match apps/console/lib/i18n.ts →
// buildLocaleCookieOptions 1:1 across each deploy environment.
//
// QA scenarios covered: 2.5-UNIT-046 (TS↔Go attribute parity table).
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/he-api/he-api/apps/api-gateway/internal/handlers"
)

// localeCookieAttrTable enumerates the expected Set-Cookie attributes per
// deploy environment. Mirrors buildLocaleCookieOptions in apps/console/lib/
// i18n.ts. If the TS helper changes, this table MUST stay in lockstep —
// catch drift via this single static table.
func TestSetLocaleCookie_AttributeParityAcrossEnv(t *testing.T) {
	t.Parallel()

	type want struct {
		domain   string
		secure   bool
		maxAge   int
		sameSite string
		path     string
		httpOnly bool
	}

	cases := []struct {
		name string
		env  handlers.DeployEnv
		w    want
	}{
		{
			name: "production",
			env:  handlers.EnvProduction,
			w: want{
				// Go's http.SetCookie strips the leading dot from Domain when
				// rendering Set-Cookie (RFC 6265 — Domain=foo.com and
				// Domain=.foo.com are semantically equivalent for browser
				// matching). The TS helper writes the dotted form for
				// readability; the wire-level normalisation preserves BR-3.5
				// attribute parity (same browser scope).
				domain:   "he-api.com",
				secure:   true,
				maxAge:   31536000,
				sameSite: "Lax",
				path:     "/",
				httpOnly: false, // client-readable per Story 2.1
			},
		},
		{
			name: "staging",
			env:  handlers.EnvStaging,
			w: want{
				domain:   "staging.he-api.com", // see production note
				secure:   true,
				maxAge:   31536000,
				sameSite: "Lax",
				path:     "/",
				httpOnly: false,
			},
		},
		{
			name: "development",
			env:  handlers.EnvDevelopment,
			w: want{
				domain:   "", // omitted in dev (localhost)
				secure:   false,
				maxAge:   31536000,
				sameSite: "Lax",
				path:     "/",
				httpOnly: false,
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			handlers.SetLocaleCookie(rec, "zh-CN", tc.env)

			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != "he_locale" {
				t.Errorf("Name = %q, want he_locale", c.Name)
			}
			if c.Value != "zh-CN" {
				t.Errorf("Value = %q, want zh-CN", c.Value)
			}
			if c.Path != tc.w.path {
				t.Errorf("Path = %q, want %q", c.Path, tc.w.path)
			}
			if c.Domain != tc.w.domain {
				t.Errorf("Domain = %q, want %q", c.Domain, tc.w.domain)
			}
			if c.Secure != tc.w.secure {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.w.secure)
			}
			if c.HttpOnly != tc.w.httpOnly {
				t.Errorf("HttpOnly = %v, want %v (client must read via document.cookie)", c.HttpOnly, tc.w.httpOnly)
			}
			if c.MaxAge != tc.w.maxAge {
				t.Errorf("MaxAge = %d, want %d (365 days)", c.MaxAge, tc.w.maxAge)
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite = %v, want Lax", c.SameSite)
			}
			_ = tc.w.sameSite // documentation field
		})
	}
}

// Scenario: 2.5-BLIND-RESOURCE-003 — header rendering smoke: confirm the
// raw Set-Cookie line is well-formed (parseable by net/http again, no
// stray attributes that next-intl can't read).
func TestSetLocaleCookie_HeaderIsRoundTrippable(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	handlers.SetLocaleCookie(rec, "ar", handlers.EnvProduction)

	raw := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(raw, "he_locale=ar;") {
		t.Fatalf("Set-Cookie does not lead with he_locale=ar; got %q", raw)
	}
	// Required attributes appear in the raw header (Go normalises dotted
	// domains — see attribute-parity test note).
	for _, want := range []string{"Path=/", "Domain=he-api.com", "Max-Age=31536000", "Secure", "SameSite=Lax"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie missing attribute %q in %q", want, raw)
		}
	}
	// HttpOnly MUST be absent (BR-3.5).
	if strings.Contains(raw, "HttpOnly") {
		t.Errorf("Set-Cookie unexpectedly carries HttpOnly: %q", raw)
	}
}
