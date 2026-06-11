'use server';

/**
 * Story 9.1 AC2/AC3 — getUsageSummary Server Action (BFF). GET
 * {gateway}/v1/me/usage/summary forwarding the inbound he_access cookie; the
 * gateway URL is server-env only (never client-exposed — 5.5 BR-L-1). cache:
 * 'no-store' (usage is mutable per-second). 401 → unauthorized sentinel so the
 * page redirects to /signin (BR-RD-3). 503 → unavailable; shape drift →
 * malformed. [getUsageSeries DEFERRED to 9.1b — Architect H-4.]
 */

import { cookies } from 'next/headers';

import { UsageSummarySchema, type UsageSummary } from '@/lib/api/me-usage';

export type GetUsageSummaryResult =
  | { ok: true; summary: UsageSummary }
  | { ok: false; unauthorized?: boolean; error: { code: string } };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

export async function getUsageSummary(): Promise<GetUsageSummaryResult> {
  let cookieHeader = '';
  try {
    const jar = await cookies();
    cookieHeader = jar
      .getAll()
      .map((c) => `${c.name}=${c.value}`)
      .join('; ');
  } catch {
    /* cookies() throws outside a request context */
  }

  let res: Response;
  try {
    res = await fetch(`${gatewayBaseURL().replace(/\/$/, '')}/v1/me/usage/summary`, {
      method: 'GET',
      headers: {
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
      },
      cache: 'no-store',
    });
  } catch {
    return { ok: false, error: { code: 'dashboard.errors.generic' } };
  }

  if (res.status === 200) {
    const body = await res.json().catch(() => null);
    const parsed = UsageSummarySchema.safeParse(body);
    if (!parsed.success) {
      // eslint-disable-next-line no-console
      console.warn('[me-usage] getUsageSummary shape drift', parsed.error.issues);
      return { ok: false, error: { code: 'dashboard.errors.malformed' } };
    }
    return { ok: true, summary: parsed.data };
  }

  if (res.status === 401) {
    // Expired/missing cookie — the page redirects to /signin (BR-RD-3).
    return { ok: false, unauthorized: true, error: { code: 'dashboard.errors.generic' } };
  }

  // Read the envelope code so a ClickHouse outage surfaces the right message.
  let envelope: { error?: { code?: string } } | null = null;
  try {
    envelope = await res.json();
  } catch {
    /* non-JSON */
  }
  if (envelope?.error?.code === '503_clickhouse_unavailable') {
    return { ok: false, error: { code: 'dashboard.errors.unavailable' } };
  }
  return { ok: false, error: { code: 'dashboard.errors.generic' } };
}
