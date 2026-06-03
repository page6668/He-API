'use server';

/**
 * Story 5.2 T6.1 — updateMyKey Server Action (BFF stub).
 *
 * PATCH {gateway}/v1/me/keys/{api_key_id}; forwards inbound he_access +
 * he_csrf cookies. Returns the typed UpdateKeyResponse on success; maps the
 * gateway error envelopes to Console-local i18n keys.
 *
 * Story 5.2 ships ONLY this action + its Vitest test (the in-Story consumer
 * proving the wiring per Dev-Gate §14). The Story-5.5 Edit-Key Modal page
 * component is the production consumer; it invokes this then revalidatePath()
 * so the table reflects the new config.
 */

import { cookies } from 'next/headers';

import {
  UpdateKeyResponseSchema,
  type KeyConfigPatch,
  type UpdateKeyResponse,
} from '@/lib/api/me-keys';

export type UpdateKeyResult =
  | { ok: true; response: UpdateKeyResponse }
  | { ok: false; error: { code: string } };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

// UUID v4 — defence-in-depth before constructing the URL (gateway also
// validates). Mirrors the Go-side uuidV4Re + revoke-key.ts.
const UUID_V4_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

export async function updateMyKey(input: {
  api_key_id: string;
  patch: KeyConfigPatch;
}): Promise<UpdateKeyResult> {
  if (!UUID_V4_RE.test(input.api_key_id)) {
    return { ok: false, error: { code: 'account.keys.edit.errors.not_found' } };
  }

  let cookieHeader = '';
  try {
    const jar = await cookies();
    cookieHeader = jar
      .getAll()
      .map((c) => `${c.name}=${c.value}`)
      .join('; ');
  } catch {
    /* see create-key.ts — cookies() throws outside a request scope (tests) */
  }

  let res: Response;
  try {
    res = await fetch(
      `${gatewayBaseURL().replace(/\/$/, '')}/v1/me/keys/${encodeURIComponent(input.api_key_id)}`,
      {
        method: 'PATCH',
        headers: {
          'Content-Type': 'application/json',
          ...(cookieHeader ? { Cookie: cookieHeader } : {}),
          Origin: process.env.NEXT_PUBLIC_CONSOLE_ORIGIN ?? 'http://localhost:3000',
        },
        body: JSON.stringify(input.patch),
        cache: 'no-store',
      },
    );
  } catch (_err) {
    return { ok: false, error: { code: 'account.keys.edit.errors.generic' } };
  }

  if (res.status === 200) {
    const body = await res.json().catch(() => null);
    const validated = UpdateKeyResponseSchema.safeParse(body);
    if (!validated.success) {
      // eslint-disable-next-line no-console
      console.warn('[me-keys] updateMyKey shape drift', validated.error.issues);
      return { ok: false, error: { code: 'account.keys.edit.errors.generic' } };
    }
    return { ok: true, response: validated.data };
  }

  let envelope: { error?: { code?: string } } | null = null;
  try {
    envelope = await res.json();
  } catch {
    /* non-JSON */
  }
  return { ok: false, error: { code: mapErrorCode(envelope?.error?.code ?? '') } };
}

/** maps the gateway §5.1.2 envelope code → a Console-local i18n key. */
function mapErrorCode(code: string): string {
  switch (code) {
    case '404_api_key_not_found':
      return 'account.keys.edit.errors.not_found';
    case '403_account_pending_deletion':
      return 'account.keys.edit.errors.account_pending_deletion';
    case '400_invalid_request': {
      // The gateway message carries the param (scope.ip_whitelist / scope.models
      // / cap range); the modal surfaces the field-specific copy. The action
      // returns the generic-invalid bucket; the page refines by inspecting the
      // offending param when present (Story 5.5).
      return 'account.keys.edit.errors.invalid_request';
    }
    case '400_unknown_field':
      return 'account.keys.edit.errors.invalid_request';
    default:
      return 'account.keys.edit.errors.generic';
  }
}
