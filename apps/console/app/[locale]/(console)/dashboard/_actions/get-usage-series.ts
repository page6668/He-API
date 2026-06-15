'use server';

/**
 * Story 9.1b AC1/AC2 — getUsageSeries Server Action (BFF). GET
 * {gateway}/v1/me/usage/series?range&group_by, forwarding the inbound he_access
 * cookie; the gateway URL is server-env only (never client-exposed — 5.5 BR-L-1).
 * Invoked from the <UsageChart> client component on the By Day/Model/Status toggle
 * (Q-CHART-FETCH SM default = client fetch), so it round-trips per toggle as a
 * Next.js Server-Action RPC. cache: 'no-store' (usage is mutable per-second).
 *
 * group_by is clamped to the allowed enum and range to a "<N>d" token (1..90)
 * before the call — defence-in-depth over the gateway's own strict-reject; an
 * out-of-bound value falls back to the safe default rather than 400-ing the chart.
 * 401 → unauthorized sentinel (the page already gated auth on the summary load);
 * 503 / shape drift → an inline error the chart card surfaces WITHOUT taking down
 * the shipped 9.1 <UsageStatCards> band (BR-CH-4 degradation isolation).
 */

import { cookies } from 'next/headers';

import {
  SERIES_GROUP_BY,
  SERIES_DEFAULT_RANGE,
  SERIES_MAX_RANGE_DAYS,
  UsageSeriesSchema,
  type SeriesGroupBy,
  type UsageSeries,
} from '@/lib/api/me-usage';

export type GetUsageSeriesResult =
  | { ok: true; series: UsageSeries }
  | { ok: false; unauthorized?: boolean; error: { code: string } };

function gatewayBaseURL(): string {
  return process.env.HE_API_GATEWAY_URL ?? 'http://api-gateway:8080';
}

/** Clamp group_by to the allowed enum (unknown → the By Day default). */
function safeGroupBy(input: string | undefined): SeriesGroupBy {
  return (SERIES_GROUP_BY as readonly string[]).includes(input ?? '')
    ? (input as SeriesGroupBy)
    : 'day';
}

/** Clamp range to a "<N>d" token with 1 ≤ N ≤ 90 (else the 30d default). */
function safeRange(input: string | undefined): string {
  const m = /^([0-9]+)d$/.exec(input ?? '');
  if (!m) return SERIES_DEFAULT_RANGE;
  const n = Number(m[1]);
  if (!Number.isInteger(n) || n < 1 || n > SERIES_MAX_RANGE_DAYS) return SERIES_DEFAULT_RANGE;
  return `${n}d`;
}

export async function getUsageSeries(
  groupBy: string = 'day',
  range: string = SERIES_DEFAULT_RANGE,
): Promise<GetUsageSeriesResult> {
  const gb = safeGroupBy(groupBy);
  const rng = safeRange(range);

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

  const qs = new URLSearchParams({ range: rng, group_by: gb }).toString();
  let res: Response;
  try {
    res = await fetch(`${gatewayBaseURL().replace(/\/$/, '')}/v1/me/usage/series?${qs}`, {
      method: 'GET',
      headers: {
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
      },
      cache: 'no-store',
    });
  } catch {
    return { ok: false, error: { code: 'dashboard.chart.error' } };
  }

  if (res.status === 200) {
    const body = await res.json().catch(() => null);
    const parsed = UsageSeriesSchema.safeParse(body);
    if (!parsed.success) {
      // eslint-disable-next-line no-console
      console.warn('[me-usage] getUsageSeries shape drift', parsed.error.issues);
      return { ok: false, error: { code: 'dashboard.chart.error' } };
    }
    return { ok: true, series: parsed.data };
  }

  if (res.status === 401) {
    // Expired/missing cookie — the page-level summary load handles the redirect.
    return { ok: false, unauthorized: true, error: { code: 'dashboard.chart.error' } };
  }

  // 503 (ClickHouse) or any other non-200 → the chart card shows an inline retry
  // (the rest of the dashboard is unaffected — BR-CH-4).
  return { ok: false, error: { code: 'dashboard.chart.error' } };
}
