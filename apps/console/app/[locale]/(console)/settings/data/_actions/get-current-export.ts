'use server';

// Story 2.6 AC1 BR-1.5 — Console BFF Server Action: getCurrentExport.
//
// Reads the he_access cookie server-side, calls api-gateway
// GET /v1/account/data-export/current, and returns a discriminated-union
// ActionResult the page renders. Returns `null` data when the user has
// no current export (CTA-enabled state).

import { cookies } from 'next/headers';
import { z } from 'zod';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

// Wire-shape Zod for GET response (200 + body or 200 + `null`).
const currentExportSchema = z
  .object({
    export_id: z.string(),
    status: z.enum(['pending', 'processing', 'completed', 'failed', 'expired']),
    requested_at: z.string(),
    signed_url_expires_at: z.string().nullable(),
  })
  .nullable();

export type CurrentExport = z.infer<typeof currentExportSchema>;

export type GetCurrentExportResult =
  | { kind: 'ok'; data: CurrentExport }
  | { kind: 'unauthorized' }
  | { kind: 'error' };

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function getCurrentExport(): Promise<GetCurrentExportResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }
  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/account/data-export/current`, {
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
  const raw = await res.json();
  const parsed = currentExportSchema.safeParse(raw);
  if (!parsed.success) return { kind: 'error' };
  return { kind: 'ok', data: parsed.data };
}
