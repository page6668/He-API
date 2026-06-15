// Story 10.8 AC2 (OQ-10.8-6) — env-driven Beta-mode read for the console face.
//
// The console marks itself "Beta" (§9.2 "文档与控制台明显标注 Beta") driven by the
// PUBLIC build-time env `NEXT_PUBLIC_BETA_MODE` (KISS — no client-side flag-read
// path). This is intentionally NOT wired to the runtime `flag:beta_mode` that
// gates the gateway (Q-ADMIN-BETA: the flip is Unleash-operated, He-API is
// read-only). The coupling — an env-driven badge does NOT auto-hide when Unleash
// flips beta_mode at GA — is mitigated by the go-live checklist "rebuild/redeploy
// to drop the Beta badge" step (BR-10.8.11).
//
// Parsing mirrors the gateway's lenient parseBool (featureflag/betamode.go): a
// truthy spelling enables; anything else — including unset/empty — is OFF
// (10.8-BLIND-BOUNDARY-001 fail-safe-OFF).

const TRUTHY = new Set(['1', 'true', 'on', 'enabled', 't', 'yes']);

/** Parse a raw env value as a Beta-mode flag. Unset/empty/unknown → false. */
export function parseBetaFlag(raw: string | undefined | null): boolean {
  if (raw == null) return false;
  return TRUTHY.has(raw.trim().toLowerCase());
}

/** Whether the console should present the "Beta" face, from NEXT_PUBLIC_BETA_MODE. */
export function isBetaMode(): boolean {
  return parseBetaFlag(process.env.NEXT_PUBLIC_BETA_MODE);
}
