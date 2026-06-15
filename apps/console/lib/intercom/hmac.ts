// Story 10.7 AC2 — Intercom Identity-Verification HMAC (SERVER-ONLY, BR-10.7.8).
//
// 🔒 This module uses `node:crypto` and is therefore server-only — it must NEVER
// be imported by a Client Component. It computes the anti-impersonation
// `user_hash = HMAC-SHA256(INTERCOM_SECRET, user_id)` over the IMMUTABLE id
// (user_id, NOT email). The capability is BUILT-but-DORMANT (see ./identity):
// in the default zero-PII mode no identity is sent, so this is never invoked at
// runtime — it exists for the future PO-approved compliant identity path only.

import { createHmac } from 'node:crypto';

/** HMAC-SHA256(secret, userId) → lowercase hex. Byte-stable; secret never leaves. */
export function computeUserHash(secret: string, userId: string): string {
  return createHmac('sha256', secret).update(userId, 'utf8').digest('hex');
}
