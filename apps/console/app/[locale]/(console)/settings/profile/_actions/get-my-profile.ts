'use server';

// Story 2.5 AC1 — Console BFF Server Action: getMyProfile.
//
// Reads the he_access cookie server-side, calls api-gateway GET /v1/me, and
// returns a discriminated-union ActionResult the page renders. The BFF
// layer is the *only* place the gateway URL leaks; React components import
// this action, not the gateway URL.
//
// Wire shape mirrors the Story 2.5 AC1 §Scenario body. The Zod schema below
// is defence-in-depth — auth-svc already validates the shape, but the BFF
// boundary re-validates so a misbehaving upstream can't corrupt the form.

import { cookies } from 'next/headers';
import { z } from 'zod';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';
import { locales } from '@/i18n/config';

// Story 2.5 — wire-shape Zod schema for GET /v1/me response.
const profileResponseSchema = z.object({
  user_id: z.string(),
  email: z.string().email(),
  display_name: z.string().nullable(),
  locale: z.enum(locales),
  timezone: z.string(),
  totp_enabled: z.boolean(),
  oauth_provider: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
});

export type Profile = z.infer<typeof profileResponseSchema>;

export type GetMyProfileResult =
  | { kind: 'ok'; data: Profile; etag: string }
  | { kind: 'unauthorized' }
  | { kind: 'pending_deletion' }
  | { kind: 'error' };

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function getMyProfile(): Promise<GetMyProfileResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/me`, {
      method: 'GET',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
      },
      // Profile is PII per BR-1.7 — never cache.
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'pending_deletion' };
  if (!res.ok) return { kind: 'error' };

  const raw = await res.json();
  const parsed = profileResponseSchema.safeParse(raw);
  if (!parsed.success) {
    return { kind: 'error' };
  }
  const etag = res.headers.get('etag') ?? '';
  return { kind: 'ok', data: parsed.data, etag };
}
