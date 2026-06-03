// Story 5.2 — UNIT-046..052 + SECURITY-001/002 (XFF trusted-proxy walker).
package keypolicy

import (
	"net/http"
	"net/netip"
	"testing"
)

func mkReq(remoteAddr, xff string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("ParsePrefix(%q): %v", c, err)
		}
		out = append(out, p)
	}
	return out
}

func TestResolveClientIP(t *testing.T) {
	t.Run("5.2-UNIT-049 empty trustedProxies → RemoteAddr regardless of XFF (failsafe)", func(t *testing.T) {
		r := mkReq("10.42.0.7:5555", "203.0.113.42, 1.2.3.4")
		got := ResolveClientIP(r, nil)
		if got.String() != "10.42.0.7" {
			t.Fatalf("got %s want 10.42.0.7 (failsafe must ignore XFF)", got)
		}
	})

	t.Run("5.2-SECURITY-001 direct call with spoofed XFF → header ignored, RemoteAddr wins", func(t *testing.T) {
		// RemoteAddr is a public IP NOT in the trusted set → not a proxy →
		// XFF is attacker-spoofed and must be ignored.
		r := mkReq("198.51.100.9:443", "192.168.1.5")
		got := ResolveClientIP(r, prefixes(t, "10.0.0.0/8", "172.16.0.0/12"))
		if got.String() != "198.51.100.9" {
			t.Fatalf("got %s want 198.51.100.9", got)
		}
	})

	t.Run("5.2-SECURITY-002 XFF via trusted ingress chain → leftmost non-trusted client", func(t *testing.T) {
		// Direct caller is k8s ingress (10.42.x in trusted CIDR); the XFF
		// rightmost hop is the trusted ingress; the real client is 203.0.113.42.
		r := mkReq("10.42.0.7:5555", "203.0.113.42, 10.42.0.7")
		got := ResolveClientIP(r, prefixes(t, "10.42.0.0/16"))
		if got.String() != "203.0.113.42" {
			t.Fatalf("got %s want 203.0.113.42", got)
		}
	})

	t.Run("empty XFF → RemoteAddr", func(t *testing.T) {
		r := mkReq("203.0.113.5:1234", "")
		got := ResolveClientIP(r, prefixes(t, "10.0.0.0/8"))
		if got.String() != "203.0.113.5" {
			t.Fatalf("got %s want 203.0.113.5", got)
		}
	})

	t.Run("malformed XFF hop → fall back to RemoteAddr", func(t *testing.T) {
		r := mkReq("10.0.0.1:9", "not-an-ip, 203.0.113.5")
		// rightmost 203.0.113.5 is non-trusted (10.0.0.0/8 trusted) → returns it.
		got := ResolveClientIP(r, prefixes(t, "10.0.0.0/8"))
		if got.String() != "203.0.113.5" {
			t.Fatalf("got %s want 203.0.113.5", got)
		}
	})

	t.Run("IPv6 RemoteAddr with port", func(t *testing.T) {
		r := mkReq("[2001:db8::1]:8443", "")
		got := ResolveClientIP(r, prefixes(t, "10.0.0.0/8"))
		if got.String() != "2001:db8::1" {
			t.Fatalf("got %s want 2001:db8::1", got)
		}
	})
}

func TestParseTrustedProxies(t *testing.T) {
	pfx, bad := ParseTrustedProxies("10.0.0.0/8, 172.16.0.0/12", "2001:db8::/32 , garbage/99")
	if len(pfx) != 3 {
		t.Fatalf("got %d prefixes want 3", len(pfx))
	}
	if len(bad) != 1 || bad[0] != "garbage/99" {
		t.Fatalf("bad=%v want [garbage/99]", bad)
	}
}

func TestParseTrustedProxies_AllEmpty(t *testing.T) {
	pfx, bad := ParseTrustedProxies("", "  ")
	if len(pfx) != 0 || len(bad) != 0 {
		t.Fatalf("empty env → no prefixes, no bad; got pfx=%v bad=%v", pfx, bad)
	}
}
