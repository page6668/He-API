'use client';

/**
 * Story 5.5 AC1 — ScopeChips (T1.6).
 *
 * Renders the key's model scope as chips: one chip per allowed model, or a
 * single "All models" chip when the array is empty (canonical no-restriction
 * per Story 5.2 BR-1.4). A "+N IPs" chip summarises the ip_whitelist; clicking
 * it opens the configure drawer (the caller wires `onConfigure`).
 */

import { useTranslations } from 'next-intl';

import type { KeyScope } from '@/lib/api/me-keys';

export interface ScopeChipsProps {
  scope: KeyScope;
  onConfigure?: () => void;
}

const CHIP = 'inline-flex items-center rounded-full bg-neutral-100 px-2 py-0.5 text-xs text-neutral-700';

export function ScopeChips({ scope, onConfigure }: ScopeChipsProps) {
  const t = useTranslations('account.keys');
  const { models, ip_whitelist } = scope;

  return (
    <div className="flex flex-wrap gap-1">
      {models.length === 0 ? (
        <span className={CHIP}>{t('scope.all_models')}</span>
      ) : (
        models.map((m) => (
          <span key={m} className={CHIP} title={m}>
            {m}
          </span>
        ))
      )}
      {ip_whitelist.length > 0 &&
        (onConfigure ? (
          <button type="button" onClick={onConfigure} className={`${CHIP} hover:bg-neutral-200`}>
            {t('scope.ip_count', { count: ip_whitelist.length })}
          </button>
        ) : (
          <span className={CHIP}>{t('scope.ip_count', { count: ip_whitelist.length })}</span>
        ))}
    </div>
  );
}
