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

/**
 * 设计系统 token 的字面量副本 —— Recharts 只接受 SVG 属性值,吃不到 Tailwind class。
 * 权威定义见 knowledge/taste/design-system.md;改色须与 tailwind.config.ts 同步。
 * 柱体走墨色系(数据即墨),朱砂刻意不用于大面积填充/装饰(铁律2)。
 */
const CHART_INK_SECONDARY = '#6B6862'; // 柱体
const CHART_LINE = '#E7E3DC'; // 暖褐网格线
const CHART_INK_MUTED = '#9A968E'; // 轴标
const CHART_SURFACE = '#FFFFFF';
const CHART_INK = '#1A1A18';

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
            // 选中态用墨色而非朱砂 —— 本屏唯一的朱砂留给「去 Playground 跑 A/B」(铁律2)
            className={`rounded-md border px-3 py-1 text-small transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 focus-visible:ring-offset-2 ${
              metric === m
                ? 'border-ink bg-ink text-paper'
                : 'border-line-strong bg-surface text-ink-secondary hover:border-ink-muted hover:text-ink'
            }`}
          >
            {metricLabel(m)}
          </button>
        ))}
      </div>

      <p className="mb-3 text-label text-ink-muted">
        {metricLabel(metric)} — {metricHint(metric)}
      </p>

      {/* vendor filter (P-14:439) */}
      <label className="mb-3 flex items-center gap-2">
        <span className="text-label text-ink-secondary">{t('table.vendor')}</span>
        <select
          data-testid="benchmark-filter"
          className="rounded-md border border-line-strong bg-surface px-2 py-1 text-small text-ink outline-none transition-colors duration-state ease-he focus:border-seal focus:ring-2 focus:ring-seal/15"
          value={vendorFilter}
          onChange={(e) => setVendorFilter(e.target.value as VendorFilter)}
        >
          <option value="all">{t('filter.all')}</option>
          <option value="he">He-API</option>
          <option value="reference">GPT-4 / Claude / Gemini</option>
        </select>
      </label>

      {/* Recharts SVG — decorative for AT; forced LTR so the x-axis is not mirrored under RTL */}
      <div aria-hidden="true" dir="ltr" data-testid="benchmark-bars" style={{ width: '100%', height: 320 }}>
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={chartData} margin={{ top: 8, right: 16, bottom: 8, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke={CHART_LINE} vertical={false} />
            <XAxis
              dataKey="model"
              tick={{ fontSize: 11, fill: CHART_INK_MUTED }}
              stroke={CHART_LINE}
              interval={0}
              angle={-20}
              textAnchor="end"
              height={60}
            />
            <YAxis tick={{ fontSize: 11, fill: CHART_INK_MUTED }} stroke={CHART_LINE} />
            {/* 唯一允许的阴影 token 给真浮层(design-system shape_elevation.shadow) */}
            <Tooltip
              cursor={{ fill: CHART_LINE, fillOpacity: 0.4 }}
              contentStyle={{
                background: CHART_SURFACE,
                border: `1px solid ${CHART_LINE}`,
                borderRadius: 8,
                boxShadow: '0 8px 24px rgba(26, 26, 24, 0.10)',
                fontSize: 13,
                color: CHART_INK,
              }}
            />
            <Bar dataKey="value" fill={CHART_INK_SECONDARY} name={metricLabel(metric)} />
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

      {/* 本屏唯一的朱砂 —— 主操作(design-system distinctive_rule 铁律2) */}
      <p className="mt-4">
        <a
          href={playgroundHref}
          data-testid="benchmark-open-ab"
          className="inline-block rounded-md bg-seal px-4 py-2 text-small font-medium text-white transition-colors duration-state ease-he hover:bg-seal-hover focus:outline-none focus:ring-2 focus:ring-seal/30"
        >
          {t('openABInPlayground')}
        </a>
      </p>
    </div>
  );
}
