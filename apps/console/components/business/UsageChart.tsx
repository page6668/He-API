'use client';

/**
 * Story 9.1b AC2 — <UsageChart>: the FIRST Recharts realization (tech-stack §2.1
 * Recharts 2.x; front-end-spec §5.2 / §P-4). A 30-day usage-trend line chart with
 * a By Day / By Model / By Status toggle, mounted beside the shipped 9.1
 * <UsageStatCards> band on /dashboard.
 *
 * Data feed: the getUsageSeries() Server Action (client-fetch on mount + per
 * toggle — Q-CHART-FETCH SM default). The gateway URL stays server-side (the
 * action is the BFF; 5.5 BR-L-1).
 *
 * Resilience (BR-CH-4 degradation isolation): loading → a chart skeleton; a
 * /series 5xx → an inline retry INSIDE the card (the stat-cards band stays live);
 * empty history → an EmptyState ("No usage in the last 30 days") with no NaN axis.
 *
 * a11y (BR-CH-3, WCAG 2.1 AA): the Recharts SVG is aria-hidden (non-text content)
 * and a visually-hidden <table> mirrors the series for screen readers; the toggle
 * is a labelled role=radiogroup with roving-tabindex + arrow-key navigation
 * (keyboard-only operable). RTL: the x-axis stays chronological L→R even in RTL
 * locales (the chart is force-LTR — 5.5 Q-RTL1 code-block precedent).
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

import { SERIES_GROUP_BY, type SeriesGroupBy, type UsageSeries } from '@/lib/api/me-usage';
import { getUsageSeries } from '@/app/[locale]/(console)/dashboard/_actions/get-usage-series';

export interface UsageChartProps {
  locale: string;
  /** Injectable for tests; defaults to the real Server Action. */
  fetcher?: (groupBy: string, range: string) => ReturnType<typeof getUsageSeries>;
}

type LoadState =
  | { status: 'loading' }
  | { status: 'error' }
  | { status: 'ready'; data: UsageSeries };

// A stable palette for the per-model / per-status lines (deterministic by index
// so the same dimension keeps its colour across re-renders).
const LINE_COLORS = ['#2563eb', '#16a34a', '#dc2626', '#d97706', '#7c3aed', '#0891b2'];

interface ChartShape {
  rows: Array<Record<string, string | number>>;
  lineKeys: string[];
}

/**
 * Pivot the wire series into Recharts rows. By Day → a single `requests` line;
 * By Model / By Status → one column (line) per key, indexed by bucket.
 */
function buildChart(series: UsageSeries): ChartShape {
  if (series.group_by === 'day') {
    return {
      rows: series.series.map((p) => ({ bucket: p.bucket, requests: p.requests })),
      lineKeys: ['requests'],
    };
  }
  const byBucket = new Map<string, Record<string, string | number>>();
  const keys: string[] = [];
  for (const p of series.series) {
    const k = p.key ?? 'requests';
    if (!keys.includes(k)) keys.push(k);
    const row = byBucket.get(p.bucket) ?? { bucket: p.bucket };
    row[k] = p.requests;
    byBucket.set(p.bucket, row);
  }
  return { rows: [...byBucket.values()], lineKeys: keys };
}

export function UsageChart({ locale, fetcher = getUsageSeries }: UsageChartProps) {
  const t = useTranslations('dashboard');
  const [groupBy, setGroupBy] = useState<SeriesGroupBy>('day');
  const [state, setState] = useState<LoadState>({ status: 'loading' });
  // Monotonic request token: a late response from a superseded toggle is dropped
  // so only the latest group_by ever paints (BLIND-FLOW-002 stale-response race).
  const reqSeq = useRef(0);

  const load = useCallback(
    async (gb: SeriesGroupBy) => {
      const seq = ++reqSeq.current;
      setState({ status: 'loading' });
      const res = await fetcher(gb, '30d');
      if (seq !== reqSeq.current) return; // a newer toggle won — discard
      if (res.ok) {
        setState({ status: 'ready', data: res.series });
      } else {
        setState({ status: 'error' });
      }
    },
    [fetcher],
  );

  useEffect(() => {
    void load(groupBy);
  }, [groupBy, load]);

  const selectGroup = useCallback((gb: SeriesGroupBy) => setGroupBy(gb), []);

  // Roving-tabindex arrow navigation for the radiogroup (keyboard-only operable —
  // BLIND-FLOW-001). Arrow keys move + select (standard radiogroup behaviour).
  const onToggleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const len = SERIES_GROUP_BY.length;
      const idx = SERIES_GROUP_BY.indexOf(groupBy);
      let next: SeriesGroupBy | undefined;
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
        next = SERIES_GROUP_BY[(idx + 1) % len];
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
        next = SERIES_GROUP_BY[(idx - 1 + len) % len];
      }
      if (next) {
        e.preventDefault();
        selectGroup(next);
      }
    },
    [groupBy, selectGroup],
  );

  const toggleLabel: Record<SeriesGroupBy, string> = {
    day: t('chart.byDay'),
    model: t('chart.byModel'),
    status: t('chart.byStatus'),
  };

  return (
    <section
      className="space-y-3 rounded-lg border border-neutral-200 p-4"
      aria-labelledby="usage-chart-heading"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="usage-chart-heading" className="text-sm font-medium text-neutral-700">
          {t('chart.title')}
        </h2>
        <div
          role="radiogroup"
          aria-label={t('chart.groupLabel')}
          className="inline-flex rounded-md border border-neutral-200"
          onKeyDown={onToggleKeyDown}
        >
          {SERIES_GROUP_BY.map((gb) => {
            const selected = gb === groupBy;
            return (
              <button
                key={gb}
                type="button"
                role="radio"
                aria-checked={selected}
                tabIndex={selected ? 0 : -1}
                onClick={() => selectGroup(gb)}
                className={`px-3 py-1.5 text-xs font-medium focus:outline-none focus-visible:ring-2 focus-visible:ring-blue-500 ${
                  selected ? 'bg-blue-600 text-white' : 'bg-white text-neutral-600 hover:bg-neutral-50'
                }`}
              >
                {toggleLabel[gb]}
              </button>
            );
          })}
        </div>
      </div>

      <ChartBody
        state={state}
        groupBy={groupBy}
        locale={locale}
        t={t}
        onRetry={() => void load(groupBy)}
      />
    </section>
  );
}

interface ChartBodyProps {
  state: LoadState;
  groupBy: SeriesGroupBy;
  locale: string;
  t: ReturnType<typeof useTranslations<'dashboard'>>;
  onRetry: () => void;
}

function ChartBody({ state, groupBy, locale, t, onRetry }: ChartBodyProps) {
  if (state.status === 'loading') {
    return (
      <div
        role="status"
        aria-busy="true"
        aria-live="polite"
        aria-label={t('chart.loading')}
        data-testid="usage-chart-skeleton"
        className="h-64 animate-pulse rounded bg-neutral-100"
      />
    );
  }

  if (state.status === 'error') {
    return (
      <div role="alert" className="space-y-2 rounded border border-red-300 bg-red-50 p-4 text-sm text-red-900">
        <p>{t('chart.error')}</p>
        <button
          type="button"
          onClick={onRetry}
          className="inline-block rounded bg-red-600 px-3 py-1.5 text-white hover:bg-red-700"
        >
          {t('chart.retry')}
        </button>
      </div>
    );
  }

  const series = state.data;
  if (series.series.length === 0) {
    return (
      <p data-testid="usage-chart-empty" className="flex h-64 items-center justify-center text-sm text-neutral-500">
        {t('chart.empty')}
      </p>
    );
  }

  const { rows, lineKeys } = buildChart(series);
  // For By Status the line/legend labels are localized; By Model uses the raw
  // model name; By Day is the single "requests" series.
  const lineLabel = (key: string): string => {
    if (groupBy === 'status' && (key === 'success' || key === 'error')) return t(`chart.status.${key}`);
    if (groupBy === 'day') return t('chart.axis.requests');
    return key;
  };

  return (
    <div>
      {/* The Recharts SVG is decorative for AT — the hidden <table> below is the
          accessible equivalent (BR-CH-3). dir=ltr keeps the x-axis chronological
          L→R even under an RTL locale (Q-RTL1). */}
      <div aria-hidden="true" dir="ltr" data-testid="usage-chart-figure" className="h-64 w-full">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={rows} margin={{ top: 8, right: 16, bottom: 8, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" />
            <XAxis dataKey="bucket" fontSize={11} />
            <YAxis allowDecimals={false} fontSize={11} />
            <Tooltip />
            {lineKeys.map((key, i) => (
              <Line
                key={key}
                type="monotone"
                dataKey={key}
                name={lineLabel(key)}
                stroke={LINE_COLORS[i % LINE_COLORS.length]}
                dot={false}
                isAnimationActive={false}
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </div>

      <table className="sr-only" data-testid="usage-chart-table">
        <caption>{t('chart.table.caption')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('chart.axis.date')}</th>
            <th scope="col">{t('chart.table.series')}</th>
            <th scope="col">{t('chart.axis.requests')}</th>
          </tr>
        </thead>
        <tbody>
          {series.series.map((p, i) => (
            <tr key={`${p.bucket}-${p.key ?? 'requests'}-${i}`}>
              <td>{p.bucket}</td>
              <td>{lineLabel(p.key ?? 'requests')}</td>
              <td>{new Intl.NumberFormat(locale).format(p.requests)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
