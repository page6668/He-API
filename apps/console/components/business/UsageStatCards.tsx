'use client';

/**
 * Story 9.1 AC3 — the §P-4 stat-card band: 3 periods (今日/本月/季度) × 4 metrics
 * (请求数/成功率/Token/消费). Per BR-UI-5 each period is a labelled <dl> region
 * (metric→value semantics) under an <h2> sub-heading. success_rate renders as a
 * locale percent, or "—" when null (requests=0) — NEVER NaN (BR-UI-2). Counts use
 * Intl.NumberFormat (grouped, locale digit script); 消费 is the billed
 * usage_ledger SUM (H-1-R) rendered via the Story-5.5 money helper (BR-UI-3), or
 * "—" when null (usage_ledger read error — R2-4 independent degradation: the
 * other three cards stay live). [The trend chart is DEFERRED to 9.1b — H-4.]
 */

import { useTranslations } from 'next-intl';

import { formatDecimal } from '@/lib/api/money';
import { PERIOD_ORDER, type PeriodKey, type UsagePeriod, type UsageSummary } from '@/lib/api/me-usage';

export interface UsageStatCardsProps {
  summary: UsageSummary;
  locale: string;
}

const EM_DASH = '—';

function formatCount(n: number, locale: string): string {
  try {
    return new Intl.NumberFormat(locale).format(n);
  } catch {
    return String(n);
  }
}

/** success_rate is a "[0,1]" string-decimal or null. Null → "—"; else percent. */
function formatSuccessRate(rate: string | null, locale: string): string {
  if (rate === null) return EM_DASH;
  const n = Number(rate);
  if (!Number.isFinite(n)) return EM_DASH; // defensive: never NaN/Inf
  try {
    return new Intl.NumberFormat(locale, {
      style: 'percent',
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }).format(n);
  } catch {
    return `${(n * 100).toFixed(2)}%`;
  }
}

export function UsageStatCards({ summary, locale }: UsageStatCardsProps) {
  const t = useTranslations('dashboard');

  const isEmpty = PERIOD_ORDER.every((p) => summary[p].requests === 0);

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        {PERIOD_ORDER.map((periodKey) => (
          <PeriodCard
            key={periodKey}
            periodKey={periodKey}
            period={summary[periodKey]}
            periodLabel={t(`periods.${periodKey}`)}
            locale={locale}
            t={t}
          />
        ))}
      </div>
      {isEmpty && (
        <p className="text-sm text-neutral-600" data-testid="usage-empty-hint">
          {t('empty.hint')}
        </p>
      )}
    </div>
  );
}

interface PeriodCardProps {
  periodKey: PeriodKey;
  period: UsagePeriod;
  periodLabel: string;
  locale: string;
  t: ReturnType<typeof useTranslations<'dashboard'>>;
}

function PeriodCard({ periodKey, period, periodLabel, locale, t }: PeriodCardProps) {
  const rows: Array<{ key: string; label: string; value: string }> = [
    { key: 'requests', label: t('metrics.requests'), value: formatCount(period.requests, locale) },
    { key: 'successRate', label: t('metrics.successRate'), value: formatSuccessRate(period.success_rate, locale) },
    { key: 'tokens', label: t('metrics.tokens'), value: formatCount(period.tokens.total, locale) },
    {
      key: 'cost',
      label: t('metrics.cost'),
      // null → "—" (usage_ledger unavailable, R2-4); else the billed money SUM.
      value: period.cost_usd === null ? EM_DASH : formatDecimal(period.cost_usd, locale, 'USD'),
    },
  ];

  return (
    <section className="rounded-lg border border-neutral-200 p-4" aria-labelledby={`usage-${periodKey}-heading`}>
      <h2 id={`usage-${periodKey}-heading`} className="mb-3 text-sm font-medium text-neutral-700">
        {periodLabel}
      </h2>
      <dl className="space-y-2">
        {rows.map((row) => (
          <div key={row.key} className="flex items-baseline justify-between gap-2">
            <dt className="text-xs text-neutral-500">{row.label}</dt>
            <dd
              className="text-lg font-semibold tabular-nums"
              aria-label={`${periodLabel} ${row.label}: ${row.value}`}
            >
              {row.value}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
