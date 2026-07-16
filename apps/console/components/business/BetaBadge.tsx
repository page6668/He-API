'use client';

// Story 10.8 AC2 (§9.2, BR-10.8.9, OQ-10.8-6) — the console's outward "Beta"
// marking. Renders a small, localized badge in the authed shell when the console
// is built in Beta mode (NEXT_PUBLIC_BETA_MODE), and renders NOTHING otherwise
// (GA face). It is display-only: it does NOT read or flip the runtime beta_mode
// gate (Q-ADMIN-BETA — that flip is Unleash-operated; this repo is read-only).
//
// QA: 10.8-UNIT-010 (renders ON) / -011 (hidden OFF) / -BLIND-BOUNDARY-001
// (unset/empty env → hidden, no crash) / -BLIND-FLOW-001 (env-driven → does not
// auto-hide on a runtime GA flip; the go-live checklist step is the mitigation).

import { useTranslations } from 'next-intl';

import { isBetaMode } from '@/lib/beta/mode';

export function BetaBadge() {
  const t = useTranslations('beta');

  // env-driven: hidden on the GA face (and on any unset/empty/unknown value).
  if (!isBetaMode()) return null;

  return (
    <span
      role="status"
      aria-label={t('badge.ariaLabel')}
      className="inline-flex items-center rounded-full border border-ochre/30 bg-ochre/5 px-2 py-0.5 text-label text-ochre"
    >
      {t('badge.label')}
    </span>
  );
}
