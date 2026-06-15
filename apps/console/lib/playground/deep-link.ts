/**
 * Story 10.6 — "Run in Playground" deep-link `#fragment` parser (BR-10.6.6,
 * OQ-10.6-4). Closes 10.5 OQ-10.5-5.
 *
 * SECURITY (the headline reason this is a unit): the fragment payload may contain
 * ARBITRARY user code from a documentation page. This parser:
 *   - validates `model ∈ catalogue` and type-checks params;
 *   - returns code (system/user) as PLAIN STRINGS for literal editor insertion;
 *   - NEVER eval()s, never `new Function(...)`, never executes the payload;
 *   - fails CLOSED — any malformed / oversized / non-UTF8 / unknown-model payload
 *     is ignored (returns null), never throwing or crashing the page.
 *
 * Wire format: a URL fragment `#prefill=<encodeURIComponent(JSON.stringify(payload))>`
 * (fragment, not query — the payload never reaches server logs; OQ-10.6-4). The
 * payload shape: { model, system?, user?, temperature?, maxTokens? }.
 */
import { isCatalogueModel } from '@/lib/catalogue/pricing';

export interface PlaygroundPrefill {
  model: string;
  system?: string;
  user?: string;
  temperature?: number;
  maxTokens?: number;
}

/** Hard cap on the encoded fragment length (defence against oversized payloads). */
const MAX_FRAGMENT_LEN = 8192;
/** Hard cap on an individual code field (system/user) after decode. */
const MAX_CODE_LEN = 16384;

function isFiniteNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v);
}

/**
 * Parse a location hash (e.g. "#prefill=...") into a validated prefill, or null.
 * NEVER throws. NEVER executes the payload.
 */
export function parseDeepLinkFragment(hash: string | null | undefined): PlaygroundPrefill | null {
  try {
    if (typeof hash !== 'string' || hash.length === 0) return null;
    const frag = hash.startsWith('#') ? hash.slice(1) : hash;
    if (frag.length === 0 || frag.length > MAX_FRAGMENT_LEN) return null;

    const params = new URLSearchParams(frag);
    const raw = params.get('prefill');
    if (!raw || raw.length > MAX_FRAGMENT_LEN) return null;

    const decoded = decodeURIComponent(raw); // throws on non-UTF8 / malformed % — caught below
    const parsed: unknown = JSON.parse(decoded); // pure parse — NOT eval
    if (typeof parsed !== 'object' || parsed === null) return null;

    const obj = parsed as Record<string, unknown>;

    // model is REQUIRED and MUST be a known catalogue model — else ignore wholesale.
    if (typeof obj.model !== 'string' || !isCatalogueModel(obj.model)) return null;

    const out: PlaygroundPrefill = { model: obj.model };

    // code fields: accepted as PLAIN STRINGS only (literal text), length-capped.
    if (typeof obj.system === 'string' && obj.system.length <= MAX_CODE_LEN) out.system = obj.system;
    if (typeof obj.user === 'string' && obj.user.length <= MAX_CODE_LEN) out.user = obj.user;

    // numeric params: type + range checked; a wrong-typed value is silently dropped.
    if (isFiniteNumber(obj.temperature) && obj.temperature >= 0 && obj.temperature <= 2) {
      out.temperature = obj.temperature;
    }
    if (isFiniteNumber(obj.maxTokens) && Number.isInteger(obj.maxTokens) && obj.maxTokens > 0) {
      out.maxTokens = obj.maxTokens;
    }

    return out;
  } catch {
    // Malformed encoding / JSON / non-UTF8 → ignore gracefully (BLIND-BOUNDARY-004).
    return null;
  }
}

/** Build a shareable deep-link fragment for a prefill (used by docs "Run in Playground"). */
export function buildDeepLinkFragment(prefill: PlaygroundPrefill): string {
  return `#prefill=${encodeURIComponent(JSON.stringify(prefill))}`;
}
