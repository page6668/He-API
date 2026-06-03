// Story 5.2 T2.3 — the three pure enforcement predicates.
//
// Each is deterministic given its inputs (trivially unit-testable, no I/O):
//   - CheckIPWhitelist (AC2): client_ip ∈ scope.ip_whitelist OR whitelist empty
//   - CheckModelScope   (AC3): request.model ∈ scope.models OR scope empty
//   - CheckMonthlyCap   (AC4): current_month_cost_usd < monthly_cost_cap_usd
//
// Monetary comparison uses math/big.Rat (stdlib exact-decimal) rather than
// shopspring/decimal — the latter is not vendored in this offline module
// graph; big.Rat parses decimal strings exactly and compares without the
// float64 precision loss BR-4.5 forbids.
package keypolicy

import (
	"math/big"
	"net/netip"
)

// CheckIPWhitelist returns allowed=true when the whitelist is empty (BR-2.1
// "any IP allowed") OR clientIP matches any entry. Each entry is parsed as a
// CIDR (netip.ParsePrefix) or a bare address (netip.ParseAddr). A malformed
// entry is skipped (it cannot match) — entries are validated at write time
// (AC1), so a malformed entry here indicates corruption, not a live input.
// CIDR matching is family-strict per BR-2.3 (Prefix.Contains handles this).
func CheckIPWhitelist(clientIP netip.Addr, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	for _, entry := range whitelist {
		if pfx, err := netip.ParsePrefix(entry); err == nil {
			if pfx.Contains(clientIP) {
				return true
			}
			continue
		}
		if addr, err := netip.ParseAddr(entry); err == nil {
			if addr == clientIP {
				return true
			}
		}
	}
	return false
}

// CheckModelScope returns allowed=true when scope is empty (BR-3.1 "all
// models allowed") OR model exactly matches an entry. Matching is
// case-sensitive per BR-3.3 (parity with the models-registry canonical form).
func CheckModelScope(model string, scope []string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, m := range scope {
		if m == model {
			return true
		}
	}
	return false
}

// CheckMonthlyCap returns allowed=true when cap is empty/NULL (BR-4.1 "no
// cap") OR current < cap. Both arguments are string-decimals; a missing
// counter is the empty string and is treated as "0" (BR-4.4). A malformed
// cap string returns allowed=true (fail-open — the cap is a soft throttle,
// not a hard boundary, per Q-F). The comparison is exact (big.Rat).
func CheckMonthlyCap(currentUSD, capUSD string) bool {
	if capUSD == "" {
		return true
	}
	cap, ok := new(big.Rat).SetString(capUSD)
	if !ok {
		return true // unparseable cap → fail-open
	}
	cur := new(big.Rat)
	if currentUSD != "" {
		if parsed, ok := new(big.Rat).SetString(currentUSD); ok {
			cur = parsed
		}
	}
	// allowed iff current < cap (equal-to-cap denies per BR-4.2).
	return cur.Cmp(cap) < 0
}
