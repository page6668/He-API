// Story 10.7 AC2 — Intercom Identity-Verification BFF (DORMANT, server-only).
//
// 🔴 OQ-2: the identity-bearing path is PO-gated. While dormant (the default),
// this route is UNREACHABLE (404) — no identity is signed in zero-PII mode.
// When the future compliant path is explicitly enabled, it computes the
// anti-impersonation `user_hash = HMAC-SHA256(INTERCOM_SECRET, user_id)` and
// returns ONLY `{ user_hash }`. The secret is read server-side only and is
// never echoed to the client (BR-10.7.8).

import { NextResponse } from 'next/server';

import { getIntercomSecret, isIdentityVerificationEnabled } from '@/lib/intercom/identity';
import { computeUserHash } from '@/lib/intercom/hmac';

// Never statically rendered — this is a credential-handling server seam.
export const dynamic = 'force-dynamic';

export async function POST(request: Request): Promise<Response> {
  // Dormant by default — unreachable while the identity-bearing path is unapproved.
  if (!isIdentityVerificationEnabled()) {
    return new NextResponse(null, { status: 404 });
  }

  const secret = getIntercomSecret();
  if (!secret) {
    // Flag on but secret not provisioned → unavailable (never a fake-green).
    return NextResponse.json({ error: 'identity_unavailable' }, { status: 500 });
  }

  let body: unknown;
  try {
    body = await request.json();
  } catch {
    return NextResponse.json({ error: 'bad_request' }, { status: 400 });
  }

  const userId = (body as { user_id?: unknown } | null)?.user_id;
  if (typeof userId !== 'string' || userId.length === 0) {
    return NextResponse.json({ error: 'bad_request' }, { status: 400 });
  }

  try {
    const user_hash = computeUserHash(secret, userId);
    return NextResponse.json({ user_hash });
  } catch {
    // ERROR-011 — log excludes the secret; client degrades to anonymous.
    console.error('[intercom-hash] identity hash computation failed');
    return NextResponse.json({ error: 'identity_hash_failed' }, { status: 500 });
  }
}
