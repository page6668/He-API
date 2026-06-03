// Story 5.4 — 5.4-UNIT-031..035 (resolveDisplayName fallback chain, BR-2.5).
package handlers

import "testing"

func TestResolveDisplayName(t *testing.T) {
	cases := []struct {
		name        string
		email       string
		displayName string
		want        string
	}{
		{"display_name present wins", "alex@example.com", "Alex Doe", "Alex Doe"},
		{"empty display_name → email local-part", "alex@example.com", "", "alex"},
		{"unicode email local-part", "用户@example.com", "", "用户"},
		{"degenerate email (no local-part) → empty", "@example.com", "", ""},
		{"empty email + empty display_name → empty", "", "", ""},
		{"no at-sign → empty", "garbage", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDisplayName(tc.email, tc.displayName); got != tc.want {
				t.Fatalf("resolveDisplayName(%q,%q)=%q want %q", tc.email, tc.displayName, got, tc.want)
			}
		})
	}
}
