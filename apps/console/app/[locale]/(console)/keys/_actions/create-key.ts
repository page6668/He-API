'use server';

/**
 * Story 5.1 T7.1 — createMyKey Server Action.
 *
 * Posts to {gateway}/v1/me/keys with the inbound he_access + he_csrf
 * cookies forwarded. On success returns the plaintext payload to the
 * caller (Page Component navigates to the Story-5.5 one-time-display
 * sub-page). On error maps the canonical §5.1.2 envelope code to a
 * Console-local i18n key.
 *
 * Scope:
 *   - Story 5.1 ships THIS action + a Vitest test calling it directly.
 *   - Story 5.5 will build the Page Component consumer.
 *
 * Plaintext invariants (BR-1.5):
 *   - This Server Action receives the plaintext from the gateway and
 *     forwards it ONCE in the return value. NEVER persist; NEVER log.
 */

import { cookies } from 'next/headers';

import {
  CreateKeyResponseSchema,
  KeyNameSchema,
  type CreateKeyResponse,
} from '@/lib/api/me-keys';

export type CreateKeyError = {
  /** i18n key (account.keys.errors.*) the page surfaces to the user. */
  code: string;
  /** Optional Retry-After seconds for 429 envelope. */
  retryAfterSeconds?: number;
};

export type CreateKeyResult =
  | { ok: true; response: CreateKeyResponse }
  | { ok: false; error: CreateKeyError };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

/**
 * createMyKey posts `{name}` to /v1/me/keys. Returns the typed result.
 *
 * The caller (Story-5.5 page) is responsible for navigation +
 * one-time-display UX. This action exists purely as the data-layer
 * bridge.
 */
export async function createMyKey(input: { name: string }): Promise<CreateKeyResult> {
  // Client-side preflight — gives a fast UX feedback loop. The server
  // re-validates (auth-svc ValidateKeyName is the source of truth).
  const parsed = KeyNameSchema.safeParse(input.name);
  if (!parsed.success) {
    const code = parsed.error.issues[0]?.message ?? 'account.keys.errors.name.invalid_chars';
    return { ok: false, error: { code } };
  }

  // Forward the inbound he_access + he_csrf cookies so the gateway's
  // RequireJWT + Origin-CSRF middlewares pass.
  let cookieHeader = '';
  try {
    const jar = await cookies();
    cookieHeader = jar
      .getAll()
      .map((c) => `${c.name}=${c.value}`)
      .join('; ');
  } catch {
    // cookies() throws when invoked outside a request context (e.g., the
    // Vitest direct invocation in T7.4). In tests the gateway URL is
    // mocked anyway; cookie absence is fine.
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayBaseURL().replace(/\/$/, '')}/v1/me/keys`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
        Origin: process.env.NEXT_PUBLIC_CONSOLE_ORIGIN ?? 'http://localhost:3000',
      },
      body: JSON.stringify({ name: parsed.data }),
      cache: 'no-store',
    });
  } catch (_err) {
    return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }

  if (res.status === 201) {
    const body = await res.json().catch(() => null);
    const validated = CreateKeyResponseSchema.safeParse(body);
    if (!validated.success) {
      // Shape drift — degrade gracefully per L-1 cascade.
      // eslint-disable-next-line no-console
      console.warn('[me-keys] createMyKey shape drift', validated.error.issues);
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    }
    return { ok: true, response: validated.data };
  }

  // Error envelope → i18n key mapping.
  let envelope: { error?: { code?: string } } | null = null;
  try {
    envelope = await res.json();
  } catch {
    /* non-JSON body — fall through to generic */
  }
  const code = envelope?.error?.code ?? '';
  switch (code) {
    case '400_invalid_key_name':
      return { ok: false, error: { code: 'account.keys.errors.name.invalid_chars' } };
    case '400_unknown_field':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    case '403_csrf_check_failed':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    case '403_account_pending_deletion':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    case '429_rate_limit_key_create': {
      const retryAfter = parseInt(res.headers.get('Retry-After') ?? '0', 10);
      return {
        ok: false,
        error: { code: 'account.keys.errors.rate_limited', retryAfterSeconds: retryAfter || undefined },
      };
    }
    case '502_auth_svc_unavailable':
    case '503_auth_unavailable':
    case '503_database_unavailable':
    case '500_internal_error':
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
    default:
      return { ok: false, error: { code: 'account.keys.errors.generic' } };
  }
}
