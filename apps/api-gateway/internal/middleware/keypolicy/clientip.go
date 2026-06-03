// Story 5.2 T2.2 — client-IP discovery via the trusted-proxy XFF walker.
//
// Architect Q-E (PARTIAL APPROVE 2026-05-25): the trusted-proxy CIDR set is
// sourced at server-startup from the `api-gateway-trusted-proxies` ConfigMap
// env vars CLOUDFLARE_CIDRS + INGRESS_CIDRS (parsed in cmd/server/main.go and
// passed into Options.TrustedProxies). The walker reads X-Forwarded-For as a
// list, walks rightmost→leftmost skipping any hop inside the trusted set, and
// returns the first non-trusted public IP. Empty/all-trusted XFF → RemoteAddr.
//
// Failsafe-against-misconfig: when TrustedProxies is empty (no ConfigMap
// configured), XFF is IGNORED entirely and RemoteAddr always wins — a
// public-IP attacker cannot spoof XFF to satisfy an IP whitelist.
package keypolicy

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ResolveClientIP returns the best-effort client IP per the Q-E contract.
// Pure given (r.Header["X-Forwarded-For"], r.RemoteAddr, trustedProxies) —
// trivially unit-testable. Never panics; on any parse failure it falls back
// to RemoteAddr (BR-2.9 — XFF is best-effort, never a 500).
func ResolveClientIP(r *http.Request, trustedProxies []netip.Prefix) netip.Addr {
	remote := remoteAddr(r)

	// Failsafe: no trusted proxies configured → trust nothing, use RemoteAddr.
	if len(trustedProxies) == 0 {
		return remote
	}

	// XFF is trustworthy ONLY when the DIRECT caller (RemoteAddr — the one
	// hop we cannot forge) is itself a trusted proxy. A direct connection
	// from an untrusted source carries an attacker-controlled XFF that MUST
	// be ignored (SECURITY-001). This is the crux of the Q-E contract.
	if !remote.IsValid() || !isTrusted(remote, trustedProxies) {
		return remote
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return remote
	}

	// Direct caller is trusted → walk XFF rightmost (closest hop) → leftmost,
	// skipping trusted hops. The first non-trusted address is the real client
	// (everything to its left is attacker-controlled and untrustworthy).
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(parts[i])
		if hop == "" {
			continue
		}
		addr, err := netip.ParseAddr(hop)
		if err != nil {
			// Malformed hop — stop trusting anything further left.
			return remote
		}
		if isTrusted(addr, trustedProxies) {
			continue
		}
		return addr
	}
	// Every hop was trusted (or empty) → the direct caller is the client.
	return remote
}

// remoteAddr parses r.RemoteAddr ("ip:port" or bare "ip") into a netip.Addr.
// Returns the zero Addr only when RemoteAddr is entirely unparseable (which
// callers treat as a non-matching IP — never a panic).
func remoteAddr(r *http.Request) netip.Addr {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr()
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr
	}
	return netip.Addr{}
}

// isTrusted reports whether addr falls inside any trusted-proxy prefix.
func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses comma-separated CIDR lists (CLOUDFLARE_CIDRS +
// INGRESS_CIDRS env vars) into a unioned []netip.Prefix. Individual malformed
// entries are dropped (returned in the bad slice for a startup WARN log)
// rather than crashing the process — an operator CIDR typo MUST NOT take the
// gateway down (availability > strictness per Q-E). Used by cmd/server.
func ParseTrustedProxies(values ...string) (prefixes []netip.Prefix, bad []string) {
	for _, v := range values {
		for _, raw := range strings.Split(v, ",") {
			entry := strings.TrimSpace(raw)
			if entry == "" {
				continue
			}
			pfx, err := netip.ParsePrefix(entry)
			if err != nil {
				bad = append(bad, entry)
				continue
			}
			prefixes = append(prefixes, pfx)
		}
	}
	return prefixes, bad
}
