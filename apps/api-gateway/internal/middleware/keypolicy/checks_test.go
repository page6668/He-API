// Story 5.2 — UNIT-053..065 (the three pure enforcement predicates).
package keypolicy

import (
	"net/netip"
	"testing"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", s, err)
	}
	return a
}

func TestCheckIPWhitelist(t *testing.T) {
	cases := []struct {
		name      string
		clientIP  string
		whitelist []string
		want      bool
	}{
		{"empty whitelist allows any (BR-2.1)", "10.0.0.5", nil, true},
		{"v4 CIDR match", "192.168.1.5", []string{"192.168.1.0/24"}, true},
		{"v4 CIDR no-match", "10.0.0.5", []string{"192.168.1.0/24"}, false},
		{"v6 CIDR match", "2001:db8::1", []string{"2001:db8::/32"}, true},
		{"mixed-family no-match (BR-2.3)", "10.0.0.5", []string{"2001:db8::/32"}, false},
		{"bare v4 literal match", "203.0.113.42", []string{"203.0.113.42"}, true},
		{"loopback allowed (BR-2.4)", "127.0.0.1", []string{"127.0.0.0/8"}, true},
		{"malformed entry skipped, no match", "10.0.0.5", []string{"garbage"}, false},
		{"first-of-many matches", "10.1.2.3", []string{"192.168.0.0/16", "10.0.0.0/8"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckIPWhitelist(mustAddr(t, tc.clientIP), tc.whitelist); got != tc.want {
				t.Fatalf("CheckIPWhitelist=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCheckModelScope(t *testing.T) {
	cases := []struct {
		name  string
		model string
		scope []string
		want  bool
	}{
		{"empty scope allows all (BR-3.1)", "qwen-max", nil, true},
		{"exact match", "qwen-max", []string{"qwen-max", "deepseek-v3"}, true},
		{"no match", "kimi-8k", []string{"qwen-max"}, false},
		{"case-sensitive (BR-3.3)", "Qwen-Max", []string{"qwen-max"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckModelScope(tc.model, tc.scope); got != tc.want {
				t.Fatalf("CheckModelScope=%v want %v", got, tc.want)
			}
		})
	}
}

func TestCheckMonthlyCap(t *testing.T) {
	cases := []struct {
		name    string
		current string
		cap     string
		want    bool
	}{
		{"no cap allows (BR-4.1)", "9999.99", "", true},
		{"under cap", "25.00", "50.00", true},
		{"equal-to-cap denies (BR-4.2)", "50.00", "50.00", false},
		{"over cap denies", "50.01", "50.00", false},
		{"missing counter treated as 0 (BR-4.4)", "", "50.00", true},
		{"precision: 50.005 vs 50.00 denies", "50.005", "50.00", false},
		{"unparseable cap fails-open", "10.00", "not-a-number", true},
		{"unparseable counter treated as 0", "junk", "50.00", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckMonthlyCap(tc.current, tc.cap); got != tc.want {
				t.Fatalf("CheckMonthlyCap(%q,%q)=%v want %v", tc.current, tc.cap, got, tc.want)
			}
		})
	}
}
