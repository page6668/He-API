// Story 10.7 AC2 — the ZERO-PII Intercom payload builders (OQ-2 hard contract).
//
// 🔴 Architect-ratified HARD CONTRACT (CI/review-enforced): the Intercom boot /
// update payload NEVER contains email, name, user_id, IP-derived identity,
// prompt, key, or any user content. The ONLY identifier permitted is the
// non-PII §11.5 `he_request_id` handle. One leaked attribute = a §9.1/PIPL
// data-egress incident (BR-10.7.7). These builders construct the payload from a
// fixed, allow-listed shape — user objects are structurally unreachable.

import { toLanguageOverride } from './config';
import { HE_REQUEST_ID_REGEX } from '../request-id';

/**
 * PII keys that must NEVER appear in any Intercom payload. The property test
 * (10.7-UNIT-010) asserts the serialized payload contains none of these.
 * `user_hash` is included: in the default ZERO-PII / dormant mode no identity
 * is signed, so no hash is ever sent (10.7-UNIT-012).
 */
export const FORBIDDEN_INTERCOM_KEYS = [
  'email',
  'name',
  'first_name',
  'last_name',
  'user_id',
  'user_hash',
  'phone',
  'avatar',
  'company',
  'companies',
  'ip',
  'ip_address',
  'created_at',
  'prompt',
  'content',
  'message_content',
  'api_key',
  'key',
] as const;

export interface BootPayloadInput {
  /** Public (non-secret) Intercom app id. */
  appId: string;
  /** Console locale → Intercom language_override. */
  locale: string;
}

export interface IntercomBootPayload {
  app_id: string;
  language_override: string;
}

/** Build the zero-PII Intercom boot payload — exactly two non-PII fields. */
export function buildBootPayload({ appId, locale }: BootPayloadInput): IntercomBootPayload {
  return {
    app_id: appId,
    language_override: toLanguageOverride(locale),
  };
}

export interface SupportSessionUpdate {
  he_request_id: string;
}

/**
 * Build the support-session attribute set carrying ONLY the non-PII
 * `he_request_id` handle (BR-10.7.9). A malformed handle is NOT attached (the
 * support session is never blocked) — returns null (10.7-UNIT-015).
 */
export function buildSupportSessionUpdate(requestId: string): SupportSessionUpdate | null {
  if (!HE_REQUEST_ID_REGEX.test(requestId)) return null;
  return { he_request_id: requestId };
}
