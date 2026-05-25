'use server';

/**
 * Story 5.1 T7.3 — revokeMyKey Server Action.
 *
 * DELETE {gateway}/v1/me/keys/{api_key_id}; forwards inbound he_access +
 * he_csrf cookies. Returns the typed RevokeKeyResponse on success (incl.
 * idempotent `was_already_revoked=true` path); maps error envelopes to
 * i18n keys.
 *
 * Story-5.5 page invokes this from the confirmation modal; on success
 * triggers revalidatePath() so the table reflects the revoked state.
 */

import { cookies } from 'next/headers';

import { RevokeKeyResponseSchema, type RevokeKeyResponse } from '@/lib/api/me-keys';

export type RevokeKeyResult =
  | { ok: true; response: RevokeKeyResponse }
  | { ok: false; error: { code: string } };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

// UUID v4 regex — guards against accidentally constructing a URL with
// untrusted input on the client side (defence-in-depth; the gateway also
// validates per BR-3.x). Mirrors the Go-side uuidV4Re.
const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

export async function revokeMyKey(input: { api_key_id: string }): Promise<RevokeKeyResult> {
  if (!UUID_V4_RE.test(input.api_key_id)) {
    return { ok: false, error: { code: 'account.keys.revoke.not_found' } };
  }

  let cookieHeader = '';
  try {
    const jar = await cookies();
    cookieHeader = jar
      .getAll()
      .map((c) => `${c.name}=${c.value}`)
      .join('; ');
  } catch {
    /* see create-key.ts */
  }

  let res: Response;
  try {
    res = await fetch(
      `${gatewayBaseURL().replace(/\/$/, '')}/v1/me/keys/${encodeURIComponent(input.api_key_id)}`,
      {
        method: 'DELETE',
        headers: {
          ...(cookieHeader ? { Cookie: cookieHeader } : {}),
          Origin: process.env.NEXT_PUBLIC_CONSOLE_ORIGIN ?? 'http://localhost:3000',
        },
        cache: 'no-store',
      },
    );
  } catch (_err) {
    return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }

  if (res.status === 200) {
    const body = await res.json().catch(() => null);
    const validated = RevokeKeyResponseSchema.safeParse(body);
    if (!validated.success) {
      // eslint-disable-next-line no-console
      console.warn('[me-keys] revokeMyKey shape drift', validated.error.issues);
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    }
    return { ok: true, response: validated.data };
  }

  let envelope: { error?: { code?: string } } | null = null;
  try {
    envelope = await res.json();
  } catch {
    /* non-JSON */
  }
  const code = envelope?.error?.code ?? '';
  switch (code) {
    case '404_api_key_not_found':
      return { ok: false, error: { code: 'account.keys.revoke.not_found' } };
    case '400_invalid_request':
      return { ok: false, error: { code: 'account.keys.revoke.not_found' } };
    case '403_csrf_check_failed':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    case '502_auth_svc_unavailable':
    case '503_auth_unavailable':
    case '503_database_unavailable':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    default:
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }
}
