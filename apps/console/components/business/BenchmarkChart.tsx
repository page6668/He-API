'use client';

/**
 * Story 10.6 — public Benchmark visualization (AC2). Renders the 3 metric views
 * (Quality / Cost-per-1M / Latency-P95) as grouped bars via Recharts (OQ-10.6-6),
 * with the 9.1b chart-a11y pattern: a radiogroup metric switcher (roving tabindex
 * + arrow keys), a visually-hidden data table mirror for screen readers, the SVG
 * marked aria-hidden + dir="ltr", and a vendor filter. Numbers / model ids / units
 * are LTR islands so ar RTL never mirrors them (BR-10.6.13 / BR-10.1.6-8).
 */
import { useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';

import type { BenchmarkData, BenchmarkRow } from '@/lib/api/benchmark';
import { LtrText } from '@/components/business/LtrText';

type MetricKey = 'quality' | 'cost' | 'latency';
type VendorFilter = 'all' | 'he' | 'reference';

const METRIC_KEYS: readonly MetricKey[] = ['quality', 'cost', 'latency'];
const METRIC_COLOR = '#2563eb';

function metricValue(row: BenchmarkRow, m: MetricKey): number {
  switch (m) {
    case 'quality':
      return row.quality;
    case 'cost':
      return row.costPer1M;
    case 'latency':
      return row.latencyP95Ms;
  }
}

export function BenchmarkChart({ data, playgroundHref }: { data: BenchmarkData; playgroundHref: string }) {
  const t = useTranslations('benchmark');
  const [metric, setMetric] = useState<MetricKey>('quality');
  const [vendorFilter, setVendorFilter] = useState<VendorFilter>('all');
  const radioRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const rows = useMemo(() => {
    if (vendorFilter === 'he') return data.rows.filter((r) => r.isHe);
    if (vendorFilter === 'reference') return data.rows.filter((r) => !r.isHe);
    return data.rows;
  }, [data.rows, vendorFilter]);

  const chartData = useMemo(
    () => rows.map((r) => ({ model: r.modelId, value: metricValue(r, metric) })),
    [rows, metric],
  );

  const metricLabel = (m: MetricKey) => t(`metric.${m}` as 'metric.quality');
  const metricHint = (m: MetricKey) => t(`metric.${m}Hint` as 'metric.qualityHint');

  const onRadioKeyDown = (e: React.KeyboardEvent, idx: number) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft' && e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const dir = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : -1;
    const next = (idx + dir + METRIC_KEYS.length) % METRIC_KEYS.length;
    setMetric(METRIC_KEYS[next] as MetricKey);
    radioRefs.current[next]?.focus();
  };

  return (
    <div data-testid="benchmark-chart">
      {/* metric switcher — radiogroup (a11y, 9.1b) */}
      <div role="radiogroup" aria-label={t('metric.select')} className="mb-3 flex gap-2">
        {METRIC_KEYS.map((m, i) => (
          <button
            key={m}
            ref={(el) => {
              radioRefs.current[i] = el;
            }}
            role="radio"
            aria-checked={metric === m}
            tabIndex={metric === m ? 0 : -1}
            data-testid={`benchmark-metric-${m}`}
            onClick={() => setMetric(m)}
            onKeyDown={(e) => onRadioKeyDown(e, i)}
            className={`rounded border px-3 py-1 text-sm ${metric === m ? 'bg-slate-900 text-white' : ''}`}
          >
            {metricLabel(m)}
          </button>
        ))}
      </div>

      <p className="mb-2 text-xs text-slate-500">
        {metricLabel(metric)} — {metricHint(metric)}
      </p>

      {/* vendor filter (P-14:439) */}
      <label className="mb-3 block text-sm">
        <span className="me-2">{t('filter.task')}</span>
        <select data-testid="benchmark-filter" className="rounded border px-2 py-1" value={vendorFilter} onChange={(e) => setVendorFilter(e.target.value as VendorFilter)}>
          <option value="all">{t('filter.all')}</option>
          <option value="he">He-API</option>
          <option value="reference">GPT-4 / Claude / Gemini</option>
        </select>
      </label>

      {/* Recharts SVG — decorative for AT; forced LTR so the x-axis is not mirrored under RTL */}
      <div aria-hidden="true" dir="ltr" data-testid="benchmark-bars" style={{ width: '100%', height: 320 }}>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={chartData} margin={{ top: 8, right: 16, bottom: 8, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" />
            <XAxis dataKey="model" tick={{ fontSize: 11 }} interval={0} angle={-20} textAnchor="end" height={60} />
            <YAxis tick={{ fontSize: 11 }} />
            <Tooltip />
            <Bar dataKey="value" fill={METRIC_COLOR} name={metricLabel(metric)} />
          </BarChart>
        </ResponsiveContainer>
      </div>

      {/* screen-reader data table mirror — full 3-metric table */}
      <table className="sr-only" data-testid="benchmark-table">
        <caption>{t('table.caption')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('table.model')}</th>
            <th scope="col">{t('metric.quality')}</th>
            <th scope="col">{t('metric.cost')}</th>
            <th scope="col">{t('metric.latency')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.modelId}>
              <th scope="row"><LtrText>{r.modelId}</LtrText></th>
              <td><LtrText>{r.quality}</LtrText></td>
              <td><LtrText>{`$${r.costPer1M}/1M`}</LtrText></td>
              <td><LtrText>{`${r.latencyP95Ms}ms`}</LtrText></td>
            </tr>
          ))}
        </tbody>
      </table>

      <p className="mt-4">
        <a href={playgroundHref} data-testid="benchmark-open-ab" className="text-sky-700 underline">
          {t('openABInPlayground')}
        </a>
      </p>
    </div>
  );
}
