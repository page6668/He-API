'use server';

// Story 6.5 AC1 — Console BFF Server Action: updateMyRoutingStrategy.
//
// Persists the account-level default routing strategy via PUT /v1/me/profile
// (reusing the Story-2.5 profile endpoint + its optimistic-concurrency If-Match
// contract — Q-C: no new endpoint). The 4 console choices map to the wire body:
//   passthrough → JSON null  (clear → STRATEGY_DEFAULT passthrough)
//   quality|cost|latency → the literal string
//
// The gateway derives user_id from the he_access JWT (BR1-1 IDOR — never the
// body), validates the enum at auth-svc, and write-throughs the gateway hot-path
// cache. This action is the ONLY place the gateway URL leaks (BR1-4 — BFF env).

import { cookies } from 'next/headers';
import { revalidatePath } from 'next/cache';

import { ACCESS_COOKIE } from '@/lib/auth/cookies';

// The persisted strategy values; `null` clears the default (Q-D).
export type RoutingStrategyValue = 'quality' | 'cost' | 'latency' | null;

export type UpdateRoutingStrategyResult =
  | { kind: 'ok'; etag: string }
  | { kind: 'concurrent_update' }
  | { kind: 'rate_limited'; retryAfter: number }
  | { kind: 'unauthorized' }
  | { kind: 'pending_deletion' }
  | { kind: 'validation' }
  | { kind: 'error' };

interface UpdateRoutingStrategyInput {
  value: RoutingStrategyValue;
  ifMatch: string;
  currentLocale: string;
}

function gatewayURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function updateMyRoutingStrategy(
  input: UpdateRoutingStrategyInput,
): Promise<UpdateRoutingStrategyResult> {
  const accessCookie = cookies().get(ACCESS_COOKIE);
  if (!accessCookie) return { kind: 'unauthorized' };

  // BR1-2 — the value is sent verbatim (null clears). user_id is NEVER in the
  // body; the gateway derives it from the JWT.
  let res: Response;
  try {
    res = await fetch(`${gatewayURL()}/v1/me/profile`, {
      method: 'PUT',
      headers: {
        Cookie: `${ACCESS_COOKIE}=${accessCookie.value}`,
        'Content-Type': 'application/json',
        'If-Match': input.ifMatch,
        Accept: 'application/json',
      },
      body: JSON.stringify({ default_routing_strategy: input.value }),
      cache: 'no-store',
    });
  } catch {
    return { kind: 'error' };
  }

  if (res.status === 401) return { kind: 'unauthorized' };
  if (res.status === 403) return { kind: 'pending_deletion' };
  if (res.status === 412 || res.status === 428) return { kind: 'concurrent_update' };
  if (res.status === 429) {
    const retry = parseInt(res.headers.get('retry-after') ?? '60', 10);
    return { kind: 'rate_limited', retryAfter: Number.isFinite(retry) ? retry : 60 };
  }
  if (res.status === 400) return { kind: 'validation' };
  if (!res.ok) return { kind: 'error' };

  revalidatePath(`/${input.currentLocale}/settings/profile`);
  return { kind: 'ok', etag: res.headers.get('etag') ?? '' };
}
