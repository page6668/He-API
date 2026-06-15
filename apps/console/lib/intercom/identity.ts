// Story 10.7 AC2 — Intercom Identity-Verification dormancy switch (SERVER-ONLY).
//
// 🔴 OQ-2: the Identity-Verification HMAC seam is BUILD-but-DORMANT. The default
// posture is zero-PII (no identity signed → no hash sent), so this is DISABLED.
// It activates ONLY when a future, PO-approved + legally-cleared compliant
// identity-bearing path is turned on (Intercom data-residency + DPA + 个人信息
// 出境合规). Until then the BFF hash route is unreachable (404).
//
// `INTERCOM_SECRET` is a server-only secret (NEVER NEXT_PUBLIC_*, never bundled).
// Provisioned via ExternalSecret → Vault `kv/data/he-api/alerting/*`-style path
// (BR-10.7.8 / BR-10.7.3); dev/CI leave it unset (dormant).

/**
 * True ONLY when BOTH the explicit activation flag is on AND the secret is
 * provisioned. Default (unset) → dormant. PO-gated to widen (OQ-2).
 */
export function isIdentityVerificationEnabled(): boolean {
  return (
    process.env.INTERCOM_IDENTITY_VERIFICATION === 'on' &&
    typeof process.env.INTERCOM_SECRET === 'string' &&
    process.env.INTERCOM_SECRET.length > 0
  );
}

/** Read the server-only Intercom HMAC secret; null when not provisioned. */
export function getIntercomSecret(): string | null {
  const secret = process.env.INTERCOM_SECRET;
  return typeof secret === 'string' && secret.length > 0 ? secret : null;
}
