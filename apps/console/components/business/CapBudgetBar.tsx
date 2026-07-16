'use client';

/**
 * Story 5.5 AC1 — CapBudgetBar (T1.5).
 *
 * Visual budget bar of current vs monthly cost cap. Per Architect Q-E5
 * OVERRULE, the "tripped" state is a CLIENT-SIDE HEURISTIC (current >= cap) —
 * there is no `cap_tripped` field on the wire. This accepts the documented
 * GAP-CAP-001 edge case (a cron-reset current=0 with a still-set Redis
 * sentinel renders under-threshold/no-badge), which is a known, accepted gap,
 * not a bug.
 *
 * NOTE (visual-refactor, 2026-07-16): the tone tokens below were migrated
 * from Tailwind defaults (bg-green-500/bg-amber-500/bg-red-500) to design
 * tokens (bg-ink/bg-ochre/bg-crimson) — 常态墨色,仅警戒/超限才升级为赭黄/深绛。
 * The matching assertions in `__tests__/5.5-console-keys-page-crud-config-ui.
 * test.tsx` (5.5-UNIT-006/007/008, 5.5-BLIND-DATA-002) were updated in step.
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
      <span className="text-small text-ink-muted" aria-label={t('cap.none')}>
        {t('cap.none')}
      </span>
    );
  }

  const currentNum = Number(current);
  const capNum = Number(cap);
  const ratio = capNum > 0 ? currentNum / capNum : 0;
  const tripped = currentNum >= capNum; // Q-E5 heuristic
  const pct = Math.min(100, Math.max(0, ratio * 100));

  // Meter fill: ink by default; ochre/crimson only past the warning/tripped
  // thresholds (design-system.md — accent reserved for the one seal action).
  const tone = tripped
    ? 'bg-crimson'
    : ratio >= 0.8
      ? 'bg-ochre'
      : 'bg-ink';

  const usageLabel = t('cap.usage_aria', {
    current: formatDecimal(current, locale, 'USD'),
    cap: formatDecimal(cap, locale, 'USD'),
  });

  return (
    <div className="min-w-[7rem]">
      <div
        className="h-2 w-full overflow-hidden rounded-full bg-surface-sunken"
        role="img"
        aria-label={tripped ? `${usageLabel}. ${t('cap.tripped_sr')}` : usageLabel}
      >
        <div className={cn('h-full rounded-full', tone)} style={{ width: `${pct}%` }} />
      </div>
      <div className="mt-1 flex items-center gap-1.5 text-small text-ink-secondary">
        <span className="tabular">
          {formatDecimal(current, locale, 'USD')} / {formatDecimal(cap, locale, 'USD')}
        </span>
        {tripped && (
          <span className="rounded-full border border-crimson/30 bg-crimson/5 px-1.5 py-0.5 text-label text-crimson">
            {t('cap.tripped')}
          </span>
        )}
      </div>
    </div>
  );
}
