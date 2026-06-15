/**
 * Story 9.1b — AC2: <UsageChart> + getUsageSeries BFF + UsageSeries schema.
 *
 * Implements the QA test-design skeleton (Turing, 2026-06-15). Rendering/toggle
 * scenarios inject a fake `fetcher` prop (the component's BFF seam) so they stay
 * deterministic without a network; the BFF scenario (UNIT-010) drives the real
 * getUsageSeries action with next/headers + fetch mocked. The Recharts SVG is
 * aria-hidden, so assertions target the hidden-table mirror + the radiogroup
 * toggle (BR-CH-3), never SVG geometry.
 *
 * Test Design: docs/qa/assessments/9.1b-test-design-20260615.md
 * Sibling reference: 9.1-usage-dashboard.test.tsx (the shipped <UsageStatCards>).
 */

import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, fireEvent, act } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

import { UsageChart } from '@/components/business/UsageChart';
import { UsageStatCards } from '@/components/business/UsageStatCards';
import { UsageSeriesSchema, type UsageSeries } from '@/lib/api/me-usage';

import enMessages from '@/messages/en/dashboard.json';
import zhCN from '@/messages/zh-CN/dashboard.json';
import de from '@/messages/de/dashboard.json';
import es from '@/messages/es/dashboard.json';
import fr from '@/messages/fr/dashboard.json';
import ja from '@/messages/ja/dashboard.json';
import ko from '@/messages/ko/dashboard.json';
import pt from '@/messages/pt/dashboard.json';
import ru from '@/messages/ru/dashboard.json';
import ar from '@/messages/ar/dashboard.json';

// next/headers is mocked file-wide: UsageChart imports the getUsageSeries action
// (which imports cookies()), and UNIT-010 drives that action directly.
let mockCookies: Array<{ name: string; value: string }> = [];
vi.mock('next/headers', () => ({
  cookies: async () => ({ getAll: () => mockCookies }),
}));

// ---- fixtures -------------------------------------------------------------

const daySeries: UsageSeries = {
  range: '30d',
  group_by: 'day',
  series: [
    { bucket: '2026-06-09', key: null, requests: 5, total_tokens: 50 },
    { bucket: '2026-06-10', key: null, requests: 7, total_tokens: 70 },
  ],
};
const modelSeries: UsageSeries = {
  range: '30d',
  group_by: 'model',
  series: [
    { bucket: '2026-06-10', key: 'qwen-max', requests: 4, total_tokens: 40 },
    { bucket: '2026-06-10', key: 'deepseek-v3', requests: 2, total_tokens: 20 },
  ],
};
const statusSeries: UsageSeries = {
  range: '30d',
  group_by: 'status',
  series: [
    { bucket: '2026-06-10', key: 'success', requests: 9, total_tokens: 90 },
    { bucket: '2026-06-10', key: 'error', requests: 1, total_tokens: 0 },
  ],
};
const emptySeries: UsageSeries = { range: '30d', group_by: 'day', series: [] };

const seriesByGroup: Record<string, UsageSeries> = {
  day: daySeries,
  model: modelSeries,
  status: statusSeries,
};

type Fetcher = (gb: string, range: string) => Promise<
  { ok: true; series: UsageSeries } | { ok: false; unauthorized?: boolean; error: { code: string } }
>;
type AnyFetcher = Fetcher | ReturnType<typeof vi.fn>;

function okFetcher(map: Record<string, UsageSeries> = seriesByGroup) {
  return vi.fn(async (gb: string) => ({ ok: true as const, series: map[gb] ?? daySeries }));
}

function renderChart(fetcher: AnyFetcher | unknown, locale = 'en', messages: Record<string, unknown> = enMessages) {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ dashboard: messages } as never}>
      <UsageChart locale={locale} fetcher={fetcher as never} />
    </NextIntlClientProvider>,
  );
}

beforeEach(() => {
  mockCookies = [];
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

// ============================================================
// AC2 — BFF / contract
// ============================================================

describe('AC2: getUsageSeries Server Action + UsageSeries schema', () => {
  // 9.1b-UNIT-010 [P1] cookie passthrough + Zod parse + 401-redirect sentinel.
  test('9.1b-UNIT-010: getUsageSeries passes cookie, parses, redirects on 401', async () => {
    const { getUsageSeries } = await import(
      '@/app/[locale]/(console)/dashboard/_actions/get-usage-series'
    );

    // Happy path: cookie forwarded, query carries range+group_by, 200 → parsed.
    mockCookies = [{ name: 'he_access', value: 'tok-123' }];
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(modelSeries), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    const ok = await getUsageSeries('model', '30d');
    expect(ok.ok).toBe(true);
    if (ok.ok) expect(ok.series.group_by).toBe('model');

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toContain('/v1/me/usage/series?range=30d&group_by=model');
    expect((init.headers as Record<string, string>).Cookie).toBe('he_access=tok-123');
    expect(init.cache).toBe('no-store');

    // 401 → unauthorized sentinel (the page-level summary load owns the redirect).
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })));
    const unauth = await getUsageSeries('day');
    expect(unauth.ok).toBe(false);
    if (!unauth.ok) expect(unauth.unauthorized).toBe(true);

    // 503 → inline error (not unauthorized).
    vi.stubGlobal('fetch', vi.fn(async () =>
      new Response(JSON.stringify({ error: { code: '503_clickhouse_unavailable' } }), { status: 503 }),
    ));
    const down = await getUsageSeries('day');
    expect(down.ok).toBe(false);
    if (!down.ok) expect(down.unauthorized).toBeFalsy();
  });

  // 9.1b-UNIT-010b — group_by/range are clamped before the gateway call
  // (defence-in-depth over the gateway strict-reject; out-of-bound → safe default).
  test('9.1b-UNIT-010b: getUsageSeries clamps unknown group_by / out-of-range', async () => {
    const { getUsageSeries } = await import(
      '@/app/[locale]/(console)/dashboard/_actions/get-usage-series'
    );
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(daySeries), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await getUsageSeries('region', '500d'); // both invalid
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(url).toContain('range=30d'); // clamped to default
    expect(url).toContain('group_by=day'); // clamped to default
  });

  // 9.1b-UNIT-011 [P1] UsageSeries Zod schema byte-matches the wire.
  test('9.1b-UNIT-011: UsageSeries schema matches wire shape exactly', () => {
    // By Day: key null, cost_usd absent.
    expect(UsageSeriesSchema.safeParse(daySeries).success).toBe(true);
    // By Model: key string.
    expect(UsageSeriesSchema.safeParse(modelSeries).success).toBe(true);
    // cost_usd present as a string is accepted (shape-parity, conditional series).
    expect(
      UsageSeriesSchema.safeParse({
        range: '30d',
        group_by: 'day',
        series: [{ bucket: '2026-06-10', key: null, requests: 7, total_tokens: 70, cost_usd: '0.50' }],
      }).success,
    ).toBe(true);
    // requests as a string → reject (counts are integers).
    expect(
      UsageSeriesSchema.safeParse({
        range: '30d',
        group_by: 'day',
        series: [{ bucket: '2026-06-10', key: null, requests: '7', total_tokens: 70 }],
      }).success,
    ).toBe(false);
    // unknown group_by → reject.
    expect(UsageSeriesSchema.safeParse({ range: '30d', group_by: 'region', series: [] }).success).toBe(false);
  });
});

// ============================================================
// AC2 — rendering + toggle
// ============================================================

describe('AC2: <UsageChart> rendering & toggle', () => {
  // 9.1b-UNIT-005 [P1] renders By Day default (daily requests line + table mirror).
  test('9.1b-UNIT-005: renders 30d series, By Day default', async () => {
    const fetcher = okFetcher();
    renderChart(fetcher);

    const figure = await screen.findByTestId('usage-chart-figure');
    expect(figure).toHaveAttribute('aria-hidden', 'true');
    // Mounted with the By Day default.
    expect(fetcher).toHaveBeenCalledWith('day', '30d');

    // The hidden-table mirror carries the daily request counts.
    const table = screen.getByTestId('usage-chart-table');
    expect(within(table).getByText('5')).toBeInTheDocument();
    expect(within(table).getByText('7')).toBeInTheDocument();
  });

  // 9.1b-UNIT-006 [P1] toggle By Model → re-query group_by=model, per-model lines.
  test('9.1b-UNIT-006: toggle By Model re-queries and renders per-model lines', async () => {
    const fetcher = okFetcher();
    renderChart(fetcher);
    await screen.findByTestId('usage-chart-figure');

    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byModel }));

    await screen.findByText('qwen-max');
    expect(fetcher).toHaveBeenCalledWith('model', '30d');
    const table = screen.getByTestId('usage-chart-table');
    expect(within(table).getByText('deepseek-v3')).toBeInTheDocument();
  });

  // 9.1b-UNIT-007 [P1] toggle By Status → group_by=status, success vs error lines.
  test('9.1b-UNIT-007: toggle By Status re-queries and renders 2xx vs 4xx/5xx', async () => {
    const fetcher = okFetcher();
    renderChart(fetcher);
    await screen.findByTestId('usage-chart-figure');

    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byStatus }));

    await screen.findByText(enMessages.chart.status.success);
    expect(fetcher).toHaveBeenCalledWith('status', '30d');
    const table = screen.getByTestId('usage-chart-table');
    expect(within(table).getByText(enMessages.chart.status.error)).toBeInTheDocument();
  });

  // 9.1b-UNIT-008 [P1] toggle By Day → re-query group_by=day.
  test('9.1b-UNIT-008: toggle By Day re-queries group_by=day', async () => {
    const fetcher = okFetcher();
    renderChart(fetcher);
    await screen.findByTestId('usage-chart-figure');

    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byModel }));
    await screen.findByText('qwen-max');
    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byDay }));

    await screen.findByTestId('usage-chart-figure');
    expect(fetcher).toHaveBeenLastCalledWith('day', '30d');
  });

  // 9.1b-UNIT-009 [P2] loading → chart skeleton renders.
  test('9.1b-UNIT-009: loading shows chart skeleton', () => {
    const pending: Fetcher = () => new Promise(() => {}); // never resolves
    renderChart(pending);
    expect(screen.getByTestId('usage-chart-skeleton')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveAttribute('aria-busy', 'true');
  });
});

// ============================================================
// AC2 — accessibility (WCAG 2.1 AA, BR-CH-3)
// ============================================================

describe('AC2: <UsageChart> accessibility', () => {
  // 9.1b-UNIT-012 [P1] SVG aria-hidden + visually-hidden <table> mirror.
  test('9.1b-UNIT-012: SVG aria-hidden, hidden-table data mirror present', async () => {
    renderChart(okFetcher());
    const figure = await screen.findByTestId('usage-chart-figure');
    expect(figure).toHaveAttribute('aria-hidden', 'true');

    const table = screen.getByRole('table');
    expect(table).toHaveClass('sr-only');
    // The caption + a header row give AT the chart's data semantics. (The
    // "Requests" copy is also a body label in By Day, so target the <th> by role.)
    expect(within(table).getByText(enMessages.chart.table.caption)).toBeInTheDocument();
    expect(within(table).getByRole('columnheader', { name: enMessages.chart.axis.requests })).toBeInTheDocument();
  });

  // 9.1b-UNIT-013 [P1] toggle is a labelled role=radiogroup control group.
  test('9.1b-UNIT-013: toggle is a labelled radiogroup', () => {
    renderChart(okFetcher());
    const group = screen.getByRole('radiogroup', { name: enMessages.chart.groupLabel });
    expect(group).toBeInTheDocument();
    expect(within(group).getAllByRole('radio')).toHaveLength(3);
    // By Day is checked by default.
    expect(screen.getByRole('radio', { name: enMessages.chart.byDay })).toHaveAttribute('aria-checked', 'true');
  });

  // 9.1b-UNIT-014 [P2] RTL: x-axis stays chronological L→R in RTL locales.
  test('9.1b-UNIT-014: RTL x-axis stays L->R chronological', async () => {
    renderChart(okFetcher(), 'ar', ar);
    const figure = await screen.findByTestId('usage-chart-figure');
    // The chart figure is force-LTR so the time axis never mirrors (Q-RTL1).
    expect(figure).toHaveAttribute('dir', 'ltr');
  });
});

// ============================================================
// AC2 — Blind Spot Scenarios [BLIND-SPOT]
// ============================================================

describe('AC2: <UsageChart> [BLIND-SPOT]', () => {
  // 9.1b-BLIND-BOUNDARY-004 [P1] empty history → EmptyState; no NaN axis.
  test('[BLIND-SPOT] 9.1b-BLIND-BOUNDARY-004: empty history renders EmptyState, no NaN axis', async () => {
    const fetcher: Fetcher = async () => ({ ok: true, series: emptySeries });
    const { container } = renderChart(fetcher);

    const empty = await screen.findByTestId('usage-chart-empty');
    expect(empty).toHaveTextContent(enMessages.chart.empty);
    // No chart figure / no NaN leakage when there are zero points.
    expect(screen.queryByTestId('usage-chart-figure')).toBeNull();
    expect(container.textContent).not.toMatch(/NaN|Infinity/);
  });

  // 9.1b-BLIND-ERROR-002 [P1] /series 5xx → inline retry; StatCards band unaffected.
  test('[BLIND-SPOT] 9.1b-BLIND-ERROR-002: /series 5xx shows inline retry, StatCards band unaffected', async () => {
    const fetcher = vi.fn(async () => ({ ok: false as const, error: { code: 'dashboard.chart.error' } }));
    const summary = {
      today: { requests: 1234, success_rate: '0.9724', tokens: { prompt: 1, completion: 2, total: 3 }, cost_usd: '0.42' },
      month: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
      quarter: { requests: 0, success_rate: null, tokens: { prompt: 0, completion: 0, total: 0 }, cost_usd: '0.0000' },
    };
    render(
      <NextIntlClientProvider locale="en" messages={{ dashboard: enMessages }}>
        <UsageStatCards summary={summary} locale="en" />
        <UsageChart locale="en" fetcher={fetcher as never} />
      </NextIntlClientProvider>,
    );

    // Chart degrades to an inline alert + retry…
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(enMessages.chart.error);
    expect(within(alert).getByRole('button', { name: enMessages.chart.retry })).toBeInTheDocument();
    // …while the shipped 9.1 stat-cards band stays fully rendered (degradation isolation).
    expect(screen.getByRole('heading', { name: enMessages.periods.today })).toBeInTheDocument();
    expect(screen.getByText('1,234')).toBeInTheDocument();

    // Retry re-invokes the feed.
    fireEvent.click(within(alert).getByRole('button', { name: enMessages.chart.retry }));
    expect(fetcher.mock.calls.length).toBeGreaterThan(1);
  });

  // 9.1b-BLIND-FLOW-001 [P1] keyboard-only toggle operable.
  test('[BLIND-SPOT] 9.1b-BLIND-FLOW-001: keyboard-only toggle operable', async () => {
    const fetcher = okFetcher();
    renderChart(fetcher);
    await screen.findByTestId('usage-chart-figure');

    const group = screen.getByRole('radiogroup', { name: enMessages.chart.groupLabel });
    // Arrow keys move + select within the radiogroup (day → model → status).
    fireEvent.keyDown(group, { key: 'ArrowRight' });
    await screen.findByText('qwen-max');
    expect(fetcher).toHaveBeenLastCalledWith('model', '30d');
    expect(screen.getByRole('radio', { name: enMessages.chart.byModel })).toHaveAttribute('aria-checked', 'true');

    fireEvent.keyDown(group, { key: 'ArrowRight' });
    await screen.findByText(enMessages.chart.status.success);
    expect(fetcher).toHaveBeenLastCalledWith('status', '30d');
  });

  // 9.1b-BLIND-FLOW-002 [P2] rapid toggle → only the latest group_by renders.
  test('[BLIND-SPOT] 9.1b-BLIND-FLOW-002: rapid toggle renders only latest series', async () => {
    const resolvers: Record<string, (s: UsageSeries) => void> = {};
    const fetcher = vi.fn(
      (gb: string) =>
        new Promise<{ ok: true; series: UsageSeries }>((resolve) => {
          resolvers[gb] = (s) => resolve({ ok: true, series: s });
        }),
    );
    renderChart(fetcher as never);

    // Resolve the initial By Day load.
    await act(async () => {
      resolvers.day?.(daySeries);
    });
    await screen.findByTestId('usage-chart-figure');

    // Rapid toggle: model then status, both in-flight.
    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byModel }));
    fireEvent.click(screen.getByRole('radio', { name: enMessages.chart.byStatus }));

    // The superseded (model) response arrives LATE — it must be dropped…
    await act(async () => {
      resolvers.model?.(modelSeries);
    });
    // …then the latest (status) response paints.
    await act(async () => {
      resolvers.status?.(statusSeries);
    });

    const table = screen.getByTestId('usage-chart-table');
    expect(within(table).getByText(enMessages.chart.status.success)).toBeInTheDocument();
    expect(within(table).queryByText('qwen-max')).toBeNull(); // stale model series discarded
  });
});

// ============================================================
// AC2 — i18n (cross-locale render)
// ============================================================

describe('AC2: <UsageChart> i18n', () => {
  // 9.1b-INT-008 [P2] axis/tooltip/toggle/empty copy render across all 10 locales.
  const LOCALES: Array<[string, Record<string, unknown>]> = [
    ['en', enMessages], ['zh-CN', zhCN], ['de', de], ['es', es], ['fr', fr],
    ['ja', ja], ['ko', ko], ['pt', pt], ['ru', ru], ['ar', ar],
  ];

  for (const [locale, messages] of LOCALES) {
    test(`9.1b-INT-008: ${locale} renders localized chart copy (no raw keys)`, async () => {
      const m = messages as {
        chart: { groupLabel: string; byDay: string; byModel: string; byStatus: string; empty: string };
      };
      const fetcher: Fetcher = async () => ({ ok: true, series: emptySeries });
      renderChart(fetcher, locale, messages);

      // The three toggle labels + empty copy are all localized…
      expect(screen.getByRole('radiogroup', { name: m.chart.groupLabel })).toBeInTheDocument();
      expect(screen.getByRole('radio', { name: m.chart.byDay })).toBeInTheDocument();
      expect(screen.getByRole('radio', { name: m.chart.byModel })).toBeInTheDocument();
      expect(screen.getByRole('radio', { name: m.chart.byStatus })).toBeInTheDocument();
      expect(await screen.findByText(m.chart.empty)).toBeInTheDocument();
      // …and no raw i18n key path leaks to the DOM.
      expect(screen.queryByText(/chart\.(title|empty|byDay)/)).toBeNull();
    });
  }
});
