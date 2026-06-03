'use client';

/**
 * Story 5.5 AC1 — CapBudgetBar (T1.5).
 *
 * Visual budget bar of current vs monthly cost cap. Per Architect Q-E5
 * OVERRULE, the "tripped" state is a CLIENT-SIDE HEURISTIC (current >= cap) —
 * there is no `cap_tripped` field on the wire. This accepts the documented
 * GAP-CAP-001 edge case (a cron-reset current=0 with a still-set Redis
 * sentinel renders green/no-badge), which is a known, accepted gap, not a bug.
 */

import { useTranslations } from 'next-intl';

import { cn } from '@/lib/utils';
import { formatDecimal } from '@/lib/api/money';

export interface CapBudgetBarProps {
  /** current_month_cost_usd — canonical numeric string (e.g. "12.50"). */
  current: string;
  /** monthly_cost_cap_usd — canonical numeric string, or null for "no cap". */
  cap: string | null;
  locale: string;
}

export function CapBudgetBar({ current, cap, locale }: CapBudgetBarProps) {
  const t = useTranslations('account.keys');

  if (cap === null) {
    return (
      <span className="text-xs text-neutral-500" aria-label={t('cap.none')}>
        {t('cap.none')}
      </span>
    );
  }

  const currentNum = Number(current);
  const capNum = Number(cap);
  const ratio = capNum > 0 ? currentNum / capNum : 0;
  const tripped = currentNum >= capNum; // Q-E5 heuristic
  const pct = Math.min(100, Math.max(0, ratio * 100));

  const tone = tripped
    ? 'bg-red-500'
    : ratio >= 0.8
      ? 'bg-amber-500'
      : 'bg-green-500';

  const usageLabel = t('cap.usage_aria', {
    current: formatDecimal(current, locale, 'USD'),
    cap: formatDecimal(cap, locale, 'USD'),
  });

  return (
    <div className="min-w-[7rem]">
      <div
        className="h-2 w-full overflow-hidden rounded-full bg-neutral-200"
        role="img"
        aria-label={tripped ? `${usageLabel}. ${t('cap.tripped_sr')}` : usageLabel}
      >
        <div className={cn('h-full rounded-full', tone)} style={{ width: `${pct}%` }} />
      </div>
      <div className="mt-1 flex items-center gap-1.5 text-xs text-neutral-600">
        <span>
          {formatDecimal(current, locale, 'USD')} / {formatDecimal(cap, locale, 'USD')}
        </span>
        {tripped && (
          <span className="rounded-full bg-red-100 px-1.5 py-0.5 font-medium text-red-700">
            {t('cap.tripped')}
          </span>
        )}
      </div>
    </div>
  );
}
