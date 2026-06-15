/**
 * Story 10.6 — Playground input front-end constraints (BR-10.6 Data Validation).
 * Pure predicates the UI uses to disable the Send button / clamp params before a
 * request is built. Upstream still enforces the canonical limits; these are the
 * BOUNDARY-001/004 front-end guards (empty user → send disabled, etc.).
 */

/** Send is allowed only when the User message is non-empty (after trim). */
export function canSend(userMessage: string): boolean {
  return typeof userMessage === 'string' && userMessage.trim().length > 0;
}

/** OpenAI temperature range [0, 2]; out-of-range is a front-end constraint. */
export function isValidTemperature(t: number): boolean {
  return Number.isFinite(t) && t >= 0 && t <= 2;
}

/** max_tokens must be a positive integer. */
export function isValidMaxTokens(n: number): boolean {
  return Number.isInteger(n) && n > 0;
}
