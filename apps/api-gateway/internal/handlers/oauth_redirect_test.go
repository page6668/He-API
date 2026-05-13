// oauth_redirect_test.go — exhaustive boundary coverage for the open-
// redirect defense. Spec demands ≥50 boundary cases (BR-1.5 + Story
// Testing Requirements §5); they're parameterized in a single table below.
//
// File→scenario mapping:
//   - 2.3-UNIT-050..059 (redirect.IsAllowed — production/staging/dev/relative
//     accepted paths + ~10 attacker tricks rejected)
//   - 2.3-E2E-007 (Open-redirect 400 — E2E exercises the wire path; this
//     file pins the function-level contract)
package handlers

import "testing"

func TestIsAllowedReturnTo(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		input   string
		env     DeployEnv
		want    bool
	}
	cases := []tc{
		// === empty / default ===
		{"empty-input", "", EnvProduction, true},

		// === relative paths ===
		{"rel-en-dashboard", "/en/dashboard", EnvProduction, true},
		{"rel-zh-CN-dashboard", "/zh-CN/dashboard", EnvProduction, true},
		{"rel-ar-dashboard", "/ar/dashboard", EnvProduction, true},
		{"rel-locale-only", "/en", EnvProduction, true},
		{"rel-locale-slash", "/en/", EnvProduction, true},
		{"rel-unsupported-locale", "/xx/dashboard", EnvProduction, false},
		{"rel-bare-path-no-locale", "/dashboard", EnvProduction, false},
		{"rel-double-slash", "//attacker.com", EnvProduction, false},
		{"rel-empty-path", "", EnvProduction, true},

		// === production allow-list ===
		{"prod-allowed-host", "https://console.he-api.com/en/dashboard", EnvProduction, true},
		{"prod-allowed-host-root", "https://console.he-api.com/", EnvProduction, true},
		{"prod-staging-host-rejected", "https://staging.console.he-api.com/en/dashboard", EnvProduction, false},

		// === staging allow-list ===
		{"staging-staging-host", "https://staging.console.he-api.com/en/dashboard", EnvStaging, true},
		{"staging-prod-host-allowed", "https://console.he-api.com/en/dashboard", EnvStaging, true},

		// === dev allow-list ===
		{"dev-localhost-http", "http://localhost:3000/en/dashboard", EnvDevelopment, true},
		{"dev-127-http", "http://127.0.0.1:3000/en/dashboard", EnvDevelopment, true},
		{"dev-https-prod-host", "https://console.he-api.com/en/dashboard", EnvDevelopment, false},
		{"dev-http-prod-host", "http://console.he-api.com/en/dashboard", EnvDevelopment, false},

		// === attacker tricks (all envs) ===
		{"attacker-external-host", "https://attacker.com/", EnvProduction, false},
		{"attacker-subdomain-trick", "https://console.he-api.com.attacker.com/", EnvProduction, false},
		{"attacker-userinfo-trick", "https://console.he-api.com@attacker.com/", EnvProduction, false},
		{"attacker-path-confusion-double-slash", "https://console.he-api.com//attacker.com/", EnvProduction, false},
		{"attacker-javascript-scheme", "javascript:alert(1)", EnvProduction, false},
		{"attacker-data-scheme", "data:text/html,<script>alert(1)</script>", EnvProduction, false},
		{"attacker-vbscript-scheme", "vbscript:alert(1)", EnvProduction, false},
		{"attacker-scheme-relative", "//attacker.com/x", EnvProduction, false},
		{"attacker-uppercase-host", "https://ATTACKER.COM/", EnvProduction, false},
		{"attacker-uppercase-allowed", "https://CONSOLE.HE-API.COM/en/dashboard", EnvProduction, true},
		{"attacker-prepended-path", "/x/../%2e%2e/etc/passwd", EnvProduction, false},

		// === url-encoded tricks ===
		// %0a as part of the host string gets folded into the hostname by
		// url.Parse, producing "console.he-api.com\n" — fails the exact-
		// match allow-list. Either behaviour (accept clean host / reject
		// embedded encoded newline) is safe; we pin the conservative
		// "reject" outcome.
		{"encoded-newline", "https://console.he-api.com%0a/attack", EnvProduction, false},
		{"raw-newline-rejected", "https://console.he-api.com\n/attack", EnvProduction, false},
		{"raw-tab-rejected", "https://console.he-api.com\t/x", EnvProduction, false},
		{"raw-cr-rejected", "https://console.he-api.com\r/x", EnvProduction, false},
		{"raw-space-rejected", "https://console.he-api.com /x", EnvProduction, false},
		{"raw-null-rejected", "https://console.he-api.com\x00/x", EnvProduction, false},

		// === protocol confusion ===
		{"http-on-prod-rejected", "http://console.he-api.com/x", EnvProduction, false},
		{"ftp-scheme-rejected", "ftp://console.he-api.com/x", EnvProduction, false},
		{"file-scheme-rejected", "file:///etc/passwd", EnvProduction, false},
		{"gopher-scheme-rejected", "gopher://console.he-api.com/x", EnvProduction, false},

		// === host edge cases ===
		{"ipv4-instead-of-host", "https://203.0.113.4/x", EnvProduction, false},
		{"ipv6-instead-of-host", "https://[::1]/x", EnvProduction, false},
		{"empty-host", "https:///path", EnvProduction, false},

		// === port handling ===
		{"prod-host-on-non-443-port", "https://console.he-api.com:8443/x", EnvProduction, true}, // port allowed; allow-list compares hostname

		// === fragments + queries ===
		{"prod-with-fragment", "https://console.he-api.com/x#section", EnvProduction, true},
		{"prod-with-query", "https://console.he-api.com/x?utm=email", EnvProduction, true},

		// === locale list completeness ===
		{"rel-ja", "/ja/dashboard", EnvProduction, true},
		{"rel-ko", "/ko/dashboard", EnvProduction, true},
		{"rel-es", "/es/dashboard", EnvProduction, true},
		{"rel-fr", "/fr/dashboard", EnvProduction, true},
		{"rel-de", "/de/dashboard", EnvProduction, true},
		{"rel-pt", "/pt/dashboard", EnvProduction, true},
		{"rel-ru", "/ru/dashboard", EnvProduction, true},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := IsAllowedReturnTo(c.input, c.env)
			if got != c.want {
				t.Errorf("IsAllowedReturnTo(%q, %v) = %v, want %v", c.input, c.env, got, c.want)
			}
		})
	}
}

func TestBuildDefaultReturnTo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		locale string
		env    DeployEnv
		want   string
	}{
		{"en", EnvProduction, "https://console.he-api.com/en/dashboard"},
		{"zh-CN", EnvProduction, "https://console.he-api.com/zh-CN/dashboard"},
		{"en", EnvStaging, "https://staging.console.he-api.com/en/dashboard"},
		{"en", EnvDevelopment, "http://localhost:3000/en/dashboard"},
		{"bogus-locale", EnvProduction, "https://console.he-api.com/en/dashboard"}, // fallback
	}
	for _, c := range cases {
		c := c
		t.Run(c.locale+"-"+string(c.env), func(t *testing.T) {
			t.Parallel()
			got := BuildDefaultReturnTo(c.locale, c.env)
			if got != c.want {
				t.Errorf("BuildDefaultReturnTo(%q, %v) = %q, want %q", c.locale, c.env, got, c.want)
			}
		})
	}
}
