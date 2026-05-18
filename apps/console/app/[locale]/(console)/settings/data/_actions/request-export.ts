'use server';

// Story 2.6 AC2 — Console BFF Server Action: requestDataExport.
//
// POSTs to api-gateway /v1/account/data-export with no body (BR-2.4 —
// user_id is derived from the JWT). Returns a discriminated-union for
// the Client Component to render the right banner / toast.

import { cookies } from 'next/headers';
import { revalidatePath } from 'next/cache';
import { z } from 'zod';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

const successSchema = z.object({
  export_id: z.string(),
  status: z.enum(['pending', 'processing', 'completed', 'failed', 'expired']),
  requested_at: z.string(),
});

export type RequestExportSuccess = z.infer<typeof successSchema>;

export type RequestExportResult =
  | { kind: 'ok'; data: RequestExportSuccess }
  | { kind: 'unauthorized' }
  | { kind: 'rate_limited' }
  | { kind: 'error' };

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function requestDataExport(localeForRevalidate?: string): Promise<RequestExportResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) {
    return { kind: 'unauthorized' };
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/account/data-export`, {
      method: 'POST',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        Accept: 'application/json',
        'Content-Type': 'application/json',
        // BR-4.6 CSRF protection — Origin header for state-mutating POST.
        // The Server Action runs server-side so this hop is internal to
        // the cluster; the gateway CSRF middleware allows-lists internal
        // origins.
      },
      body: '{}', // BR-2.4 — empty body
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }
  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 429) return { kind: 'rate_limited' };
  if (!res.ok) return { kind: 'error' };

  const raw = await res.json();
  const parsed = successSchema.safeParse(raw);
  if (!parsed.success) return { kind: 'error' };

  // Re-render the page so the Server Component re-reads current-export
  // state and the CTA flips to disabled per BR-1.5.
  if (localeForRevalidate) {
    revalidatePath(`/${localeForRevalidate}/settings/data`);
  }

  return { kind: 'ok', data: parsed.data };
}
