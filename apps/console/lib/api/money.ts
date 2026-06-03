/**
 * Story 5.5 T0.5 — string-decimal money helpers for the `monthly_cost_cap_usd`
 * field (the FIRST money field on the console surface; Story 5.1 Q-Spec-4).
 *
 * Design constraints:
 *   - NO `decimal.js` dependency (Story Boundary Lock + KISS). The cap range is
 *     bounded to [0.01, 999999.99] (8 significant digits) so integer-cent
 *     arithmetic via BigInt is exact — we only parse / compare / format, never
 *     do floating-point arithmetic.
 *   - Wire shape is ALWAYS the canonical ASCII `"NN.NN"` regardless of the
 *     user's locale (Story 5.2 BR-1.6 / BR-U-5). Display is locale-aware.
 *   - Parsing is locale-aware: decimal separator, group separator, and digit
 *     script (Arabic-Indic / Extended Arabic-Indic) all vary by locale.
 */

/** Inclusive cap bounds in integer cents (Story 5.2 BR-1.6). */
const MIN_CENTS = 1n; //          $0.01
const MAX_CENTS = 99_999_999n; // $999,999.99

export type ParseDecimalError = 'invalid' | 'out_of_range';

export interface ParseDecimalResult {
  /** Canonical `"NN.NN"` string, or null when the input is empty (= no cap). */
  value: string | null;
  error: ParseDecimalError | null;
}

/**
 * Map any Arabic-Indic (U+0660–0669) or Extended Arabic-Indic / Persian
 * (U+06F0–06F9) digit to its ASCII equivalent. Other characters pass through.
 */
function normalizeDigits(input: string): string {
  let out = '';
  for (const ch of input) {
    const code = ch.codePointAt(0)!;
    if (code >= 0x0660 && code <= 0x0669) {
      out += String.fromCharCode(0x30 + (code - 0x0660));
    } else if (code >= 0x06f0 && code <= 0x06f9) {
      out += String.fromCharCode(0x30 + (code - 0x06f0));
    } else {
      out += ch;
    }
  }
  return out;
}

/** Resolve the locale's decimal + group separator characters via Intl. */
function separatorsFor(locale: string): { group: string; decimal: string } {
  let group = ',';
  let decimal = '.';
  try {
    const parts = new Intl.NumberFormat(locale).formatToParts(1234567.89);
    for (const part of parts) {
      if (part.type === 'group') group = part.value;
      else if (part.type === 'decimal') decimal = part.value;
    }
  } catch {
    /* unknown locale → keep en defaults */
  }
  return { group, decimal };
}

/** Convert a canonical ASCII decimal string ("50", "50.5", "50.00") to cents. */
function asciiDecimalToCents(ascii: string): bigint | null {
  if (!/^-?\d+(\.\d+)?$/.test(ascii)) return null;
  const negative = ascii.startsWith('-');
  const body = negative ? ascii.slice(1) : ascii;
  const [intPart = '0', fracPartRaw = ''] = body.split('.');
  if (fracPartRaw.length > 2) return null; // more than 2 decimals → invalid
  const frac = (fracPartRaw + '00').slice(0, 2);
  const cents = BigInt(intPart) * 100n + BigInt(frac);
  return negative ? -cents : cents;
}

/** Format integer cents as the canonical `"NN.NN"` string. */
function centsToCanonical(cents: bigint): string {
  const sign = cents < 0n ? '-' : '';
  const abs = cents < 0n ? -cents : cents;
  const whole = abs / 100n;
  const frac = abs % 100n;
  return `${sign}${whole.toString()}.${frac.toString().padStart(2, '0')}`;
}

/**
 * Parse a user-typed cap value (in the given locale) into a canonical
 * `"NN.NN"` string suitable for the wire. Empty input means "no cap" and
 * returns `{ value: null, error: null }`.
 */
export function parseDecimal(input: string, locale: string): ParseDecimalResult {
  const trimmed = input.trim();
  if (trimmed === '') return { value: null, error: null };

  const { group, decimal } = separatorsFor(locale);
  let normalized = normalizeDigits(trimmed);

  // Strip group separators (locale-specific char + common whitespace groupers
  // such as NBSP / narrow NBSP used by fr/ru) and the currency symbol/spaces.
  if (group) normalized = normalized.split(group).join('');
  normalized = normalized.replace(/[\s  ٬]/g, '');

  // Normalise the locale decimal separator to ASCII '.'.
  if (decimal && decimal !== '.') {
    normalized = normalized.split(decimal).join('.');
  }
  // Arabic decimal separator (U+066B) may appear even when Intl reports a
  // different one depending on the ICU build — normalise it defensively.
  normalized = normalized.split('٫').join('.');

  const cents = asciiDecimalToCents(normalized);
  if (cents === null) return { value: null, error: 'invalid' };
  if (cents < MIN_CENTS || cents > MAX_CENTS) {
    return { value: null, error: 'out_of_range' };
  }
  return { value: centsToCanonical(cents), error: null };
}

/**
 * Defensive canonicaliser: strips formatting from a plain ASCII numeric string
 * and returns `"NN.NN"` (always 2 decimal places). Throws on malformed input so
 * a programming error never silently ships a bad wire value.
 */
export function toCanonicalString(value: string): string {
  const cents = asciiDecimalToCents(value.trim());
  if (cents === null) {
    throw new Error(`toCanonicalString: not a canonical decimal: ${value}`);
  }
  return centsToCanonical(cents);
}

/**
 * Locale-aware display formatter. With a `currency` it renders a currency
 * string (e.g. en+USD → "$50.00"); without, a plain 2-decimal number in the
 * locale's digit script + separators.
 */
export function formatDecimal(
  canonicalValue: string,
  locale: string,
  currency?: string,
): string {
  const num = Number(canonicalValue);
  const options: Intl.NumberFormatOptions = currency
    ? { style: 'currency', currency }
    : { minimumFractionDigits: 2, maximumFractionDigits: 2 };
  try {
    return new Intl.NumberFormat(locale, options).format(num);
  } catch {
    return canonicalValue;
  }
}
