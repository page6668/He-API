'use server';

// Story 2.7 — account-deletion Server Actions (console BFF → api-gateway REST).
// Shared by the DeleteAccountDialog (AC1), the recovery page (AC3), and the
// (console) layout guard (AC4). user_id is NEVER sent — it is derived from the
// JWT `sub` by the gateway (BR-2.4). All requests forward the he_access cookie.

import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { z } from 'zod';

import {
  ACCESS_COOKIE,
  buildAccessCookieOptions,
  buildRefreshCookieOptions,
} from '@/lib/auth/cookies';
import type { Env } from '@/lib/i18n';

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

function consoleOrigin(): string {
  return process.env.NEXT_PUBLIC_CONSOLE_ORIGIN ?? 'http://localhost:3000';
}

function currentEnv(): Env {
  const envVar = process.env.NEXT_PUBLIC_DEPLOY_ENV ?? process.env.NODE_ENV ?? 'development';
  if (envVar === 'production') return 'production';
  if (envVar === 'staging') return 'staging';
  return 'development';
}

// --- GET /v1/account/deletion (state hydration) --------------------------

const stateSchema = z.object({
  status: z.string(),
  pending_deletion_at: z.string().nullable().optional(),
  has_password: z.boolean(),
  totp_enabled: z.boolean(),
  timezone: z.string(),
});

export type DeletionState = z.infer<typeof stateSchema>;

export type GetDeletionStateResult =
  | { kind: 'ok'; data: DeletionState }
  | { kind: 'unauthorized' }
  | { kind: 'error' };

export async function getDeletionState(): Promise<GetDeletionStateResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }
  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/account/deletion`, {
      method: 'GET',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
      },
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }
  if (res.status === 401) return { kind: 'unauthorized' };
  if (!res.ok) return { kind: 'error' };
  let raw: unknown;
  try {
    raw = await res.json();
  } catch {
    return { kind: 'error' };
  }
  const parsed = stateSchema.safeParse(raw);
  if (!parsed.success) return { kind: 'error' };
  return { kind: 'ok', data: parsed.data };
}

// --- POST /v1/account/deletion (request) ---------------------------------

export interface RequestDeletionInput {
  password?: string;
  confirm_email?: string;
  totp_code?: string;
}

export type RequestDeletionResult =
  | { kind: 'ok' } // pending_deletion (new or idempotent) → caller redirects to recovery
  | { kind: 'unauthorized' }
  | { kind: 'bad_reauth' }
  | { kind: 'rate_limited' }
  | { kind: 'not_deletable' } // 409 — suspended/locked/deleted
  | { kind: 'error' };

export async function requestAccountDeletion(reauth: RequestDeletionInput): Promise<RequestDeletionResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }
  // Build a reauth object with ONLY the provided fields (no user_id — BR-2.4).
  const reauthBody: Record<string, string> = {};
  if (reauth.password) reauthBody.password = reauth.password;
  if (reauth.confirm_email) reauthBody.confirm_email = reauth.confirm_email;
  if (reauth.totp_code) reauthBody.totp_code = reauth.totp_code;

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/account/deletion`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Origin: consoleOrigin(),
      },
      body: JSON.stringify({ reauth: reauthBody }),
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }
  switch (res.status) {
    case 200:
      return { kind: 'ok' };
    case 401:
      return { kind: 'unauthorized' };
    case 403:
      // The request endpoint only emits 403_bad_reauth (never the AC4
      // pending-deletion guard — that's other endpoints).
      return { kind: 'bad_reauth' };
    case 409:
      return { kind: 'not_deletable' };
    case 429:
      return { kind: 'rate_limited' };
    default:
      return { kind: 'error' };
  }
}

// --- POST /v1/account/deletion/cancel (reactivate) -----------------------

export type CancelDeletionResult =
  | { kind: 'ok' }
  | { kind: 'unauthorized' }
  | { kind: 'grace_expired' }
  | { kind: 'error' };

export async function cancelAccountDeletion(): Promise<CancelDeletionResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }
  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/account/deletion/cancel`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
        'Content-Type': 'application/json',
        Origin: consoleOrigin(),
      },
      body: '{}',
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }
  switch (res.status) {
    case 200:
      return { kind: 'ok' };
    case 401:
      return { kind: 'unauthorized' };
    case 410:
      return { kind: 'grace_expired' };
    default:
      return { kind: 'error' };
  }
}

// --- signout on grace-expired (AC3 410 path) -----------------------------

// AC3 error-table: a user who reaches the grace-expired state (the sweeper has
// already anonymized the account, cancel → 410) MUST be signed out, not left
// holding a stale he_access cookie until JWT TTL expiry (QA-2.7-001). Clear the
// gateway-issued session cookies locally (mirroring their attribute matrix so
// the Set-Cookie delete actually matches) and route to signin. The account is
// gone server-side; this only terminates the now-orphaned local session.
export async function signOutAfterGraceExpiry(locale: string): Promise<never> {
  const env = currentEnv();
  for (const opts of [buildAccessCookieOptions(env), buildRefreshCookieOptions(env)]) {
    cookies().set(opts.name, '', {
      path: opts.path,
      maxAge: 0,
      sameSite: opts.sameSite,
      httpOnly: opts.httpOnly,
      secure: opts.secure,
      ...(opts.domain ? { domain: opts.domain } : {}),
    });
  }
  redirect(`/${locale}/signin`);
}
