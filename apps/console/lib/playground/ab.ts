/**
 * Story 10.6 — A/B header assembly + front-end pre-validation (BR-10.6.3, 6.4).
 *
 * The X-He-AB-Models header requires EXACTLY 2 distinct CONCRETE model ids
 * (non-streaming only — enforced at send time by the UI). he-router-* meta-models
 * are rejected (the A/B contract needs concrete legs). This front-end check mirrors
 * the gateway's ParseABModels so the UI fails fast before a 400 round-trip.
 */

const AB_HEADER = 'X-He-AB-Models';

export type AbResult =
  | { ok: true; header: string; models: [string, string] }
  | { ok: false; reason: 'count' | 'duplicate' | 'router' | 'empty' };

function isRouterModel(id: string): boolean {
  return id.startsWith('he-router');
}

/** Validate + assemble the X-He-AB-Models header value from two selected models. */
export function buildAbModels(models: string[]): AbResult {
  const cleaned = models.map((m) => (m ?? '').trim()).filter((m) => m !== '');
  if (cleaned.length !== 2) {
    return { ok: false, reason: cleaned.length === 0 ? 'empty' : 'count' };
  }
  const a = cleaned[0] as string;
  const b = cleaned[1] as string;
  if (a === b) return { ok: false, reason: 'duplicate' };
  if (isRouterModel(a) || isRouterModel(b)) return { ok: false, reason: 'router' };
  return { ok: true, header: `${a},${b}`, models: [a, b] };
}

export const AB_MODELS_HEADER = AB_HEADER;
