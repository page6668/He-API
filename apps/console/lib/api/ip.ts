/**
 * Story 5.5 T3.2 — client-side IP / CIDR validator for the configure-drawer
 * IP-whitelist editor (Story 5.2 BR-1.5 parity; Q-IPV6 ruling).
 *
 * This is defence-in-depth UX feedback — the server (auth-svc, via
 * `net/netip`) is the source of truth. Rules:
 *   - accepts IPv4, IPv6, IPv4-CIDR, IPv6-CIDR
 *   - rejects degenerate `/0` CIDRs (0.0.0.0/0, ::/0) — they allow every IP
 *   - rejects IPv6 zone-ids (fe80::1%eth0) — link-local, meaningless at the
 *     gateway boundary (Q-IPV6 SM default)
 *   - rejects anything else as invalid_format
 */

export type IpRuleError =
  | 'invalid_format'
  | 'degenerate_cidr'
  | 'zone_id_rejected';

export interface IpRuleResult {
  valid: boolean;
  error: IpRuleError | null;
}

function isIPv4(addr: string): boolean {
  const octets = addr.split('.');
  if (octets.length !== 4) return false;
  return octets.every((o) => {
    if (!/^\d{1,3}$/.test(o)) return false;
    if (o.length > 1 && o.startsWith('0')) return false; // no leading zeros
    return Number(o) <= 255;
  });
}

function isIPv6(addr: string): boolean {
  if (addr.includes('%')) return false; // zone-id handled by the caller
  const hextet = /^[0-9a-fA-F]{1,4}$/;
  const halves = addr.split('::');
  if (halves.length > 2) return false;

  if (halves.length === 2) {
    // Compressed form: at most one "::".
    const head = halves[0] ? halves[0].split(':') : [];
    const tail = halves[1] ? halves[1].split(':') : [];
    if (![...head, ...tail].every((h) => hextet.test(h))) return false;
    // "::" must stand in for at least one zero group → ≤ 7 explicit groups.
    return head.length + tail.length <= 7;
  }

  const groups = addr.split(':');
  if (groups.length !== 8) return false;
  return groups.every((g) => hextet.test(g));
}

/** Validate a single whitelist row value. */
export function validateIpRule(input: string): IpRuleResult {
  const trimmed = input.trim();
  if (trimmed === '') return { valid: false, error: 'invalid_format' };

  let addr = trimmed;
  let prefix: number | null = null;
  const slash = trimmed.indexOf('/');
  if (slash >= 0) {
    addr = trimmed.slice(0, slash);
    const rawPrefix = trimmed.slice(slash + 1);
    if (!/^\d{1,3}$/.test(rawPrefix)) return { valid: false, error: 'invalid_format' };
    prefix = Number(rawPrefix);
  }

  if (addr.includes('%')) return { valid: false, error: 'zone_id_rejected' };

  const v4 = isIPv4(addr);
  const v6 = !v4 && isIPv6(addr);
  if (!v4 && !v6) return { valid: false, error: 'invalid_format' };

  if (prefix !== null) {
    const max = v4 ? 32 : 128;
    if (prefix > max) return { valid: false, error: 'invalid_format' };
    if (prefix === 0) return { valid: false, error: 'degenerate_cidr' };
  }

  return { valid: true, error: null };
}
