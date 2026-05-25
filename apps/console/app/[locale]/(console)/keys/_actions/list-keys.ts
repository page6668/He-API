'use server';

/**
 * Story 5.1 T7.2 — listMyKeys Server Action.
 *
 * GET {gateway}/v1/me/keys; forwards the inbound he_access cookie. On
 * success returns the typed ListKeysResponse for the Story-5.5 Page
 * Component to render. On error returns an error union with an i18n key.
 *
 * Plaintext invariants (BR-1.6): the list endpoint NEVER carries
 * plaintext — only key_prefix. This action does NOT need to mask
 * anything; the gateway already enforces.
 */

import { cookies } from 'next/headers';

import { ListKeysResponseSchema, type ListKeysResponse } from '@/lib/api/me-keys';

export type ListKeysResult =
  | { ok: true; response: ListKeysResponse }
  | { ok: false; error: { code: string } };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function listMyKeys(): Promise<ListKeysResult> {
  let cookieHeader = '';
  try {
    const jar = await cookies();
    cookieHeader = jar
      .getAll()
      .map((c) => `${c.name}=${c.value}`)
      .join('; ');
  } catch {
    /* see create-key.ts comment */
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayBaseURL().replace(/\/$/, '')}/v1/me/keys`, {
      method: 'GET',
      headers: {
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
      },
      cache: 'no-store',
    });
  } catch (_err) {
    return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }

  if (res.status === 200) {
    const body = await res.json().catch(() => null);
    const validated = ListKeysResponseSchema.safeParse(body);
    if (!validated.success) {
      // eslint-disable-next-line no-console
      console.warn('[me-keys] listMyKeys shape drift', validated.error.issues);
      return { ok: true, response: { object: 'list', data: [] } };
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
    case '400_unknown_field':
      // BR-2.1 strict-reject (e.g., ?user_id=...) — the caller shouldn't
      // send query params; surface a generic error.
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    case '502_auth_svc_unavailable':
    case '503_auth_unavailable':
    case '503_database_unavailable':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    default:
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }
}
