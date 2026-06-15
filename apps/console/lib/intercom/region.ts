// Story 10.7 AC2 — Intercom PRC region-gate (OQ-2, BR-10.7.7).
//
// Even an anonymous Intercom session egresses IP/cookies to a US SaaS, which is
// PIPL-sensitive under §9.1「数据不出境」. Architect ruling: PRC-region users do
// NOT boot Intercom — they are routed to an in-region support channel (飞书/email);
// Intercom serves the international face only. The country code is resolved
// server-side from the edge (Cloudflare `cf-ipcountry`) and passed to the client
// component as a prop — the boot decision itself is this pure predicate.
//
// [Source: docs/architecture/9-合规架构compliance-architecture.md §9.1:3-10]

/** ISO-3166-1 alpha-2 codes treated as PRC mainland (PIPL territory). */
export const PRC_COUNTRY_CODES: readonly string[] = ['CN'];

export function isPrcRegion(countryCode: string | null | undefined): boolean {
  if (!countryCode) return false;
  return PRC_COUNTRY_CODES.includes(countryCode.trim().toUpperCase());
}

export interface BootGateInput {
  /** Public Intercom app id; absent/empty → no widget (graceful degrade). */
  appId: string | null | undefined;
  /** Edge-resolved ISO country code (e.g. Cloudflare cf-ipcountry). */
  countryCode: string | null | undefined;
}

/**
 * Whether to boot the Intercom Messenger. False when the app id is missing
 * (BOUNDARY-010) or the request originates from a PRC region (UNIT-013).
 */
export function shouldBootIntercom({ appId, countryCode }: BootGateInput): boolean {
  if (!appId || appId.trim() === '') return false;
  if (isPrcRegion(countryCode)) return false;
  return true;
}
