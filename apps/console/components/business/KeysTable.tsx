'use client';

/**
 * Story 5.5 AC1 — KeysTable (T1.2).
 *
 * Desktop `<table>` (>=md) + mobile card stack (<md) — reuses the Story-4.7
 * CapabilityMatrix responsive breakpoint pattern. Revoked rows render at
 * opacity-50 with a "Revoked" badge and NO action buttons (configure/revoke
 * are no-ops on a terminal-revoked key, BR-L-5).
 */

import { Settings, Ban } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { cn } from '@/lib/utils';
import { readScope, type KeyEntry } from '@/lib/api/me-keys';
import { CapBudgetBar } from './CapBudgetBar';
import { ScopeChips } from './ScopeChips';

export interface KeysTableProps {
  keys: KeyEntry[];
  locale: string;
  onConfigure: (key: KeyEntry) => void;
  onRevoke: (key: KeyEntry) => void;
}

function formatDate(iso: string, locale: string): string {
  try {
    return new Intl.DateTimeFormat(locale).format(new Date(iso));
  } catch {
    return iso;
  }
}

export function KeysTable({ keys, locale, onConfigure, onRevoke }: KeysTableProps) {
  const t = useTranslations('account.keys');

  const headers = (
    <tr className="border-b border-line text-start text-label text-ink-muted">
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.name')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.prefix')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.scope')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.created')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.last_used')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.cap')}</th>
      <th className="px-3 py-2.5 text-start font-medium">{t('table.header.status')}</th>
      <th className="px-3 py-2.5 text-end font-medium">{t('table.header.actions')}</th>
    </tr>
  );

  return (
    <>
      {/* Desktop — dense instrument table: ~44px rows, 1px warm border separators, zero shadow. */}
      <div className="hidden overflow-hidden rounded-lg border border-line md:block">
        <table className="w-full border-collapse text-small">
          <thead className="sticky top-0 bg-surface">{headers}</thead>
          <tbody>
            {keys.map((key) => {
              const revoked = key.revoked_at !== null;
              const scope = readScope(key.scope);
              return (
                <tr key={key.api_key_id} className={cn('border-b border-line last:border-b-0', revoked && 'opacity-50')}>
                  <td className="px-3 py-3 text-ink">
                    <span title={key.name}>{key.name}</span>
                  </td>
                  <td className="px-3 py-3">
                    <code className="tabular text-ink-secondary">{key.key_prefix}…</code>
                  </td>
                  <td className="px-3 py-3">
                    <ScopeChips scope={scope} onConfigure={revoked ? undefined : () => onConfigure(key)} />
                  </td>
                  <td className="px-3 py-3">
                    <time dateTime={key.created_at} className="tabular text-ink-secondary">
                      {formatDate(key.created_at, locale)}
                    </time>
                  </td>
                  <td className="px-3 py-3">
                    {key.last_used_at ? (
                      <time dateTime={key.last_used_at} className="tabular text-ink-secondary">
                        {formatDate(key.last_used_at, locale)}
                      </time>
                    ) : (
                      <span className="text-ink-muted">{t('last_used.never')}</span>
                    )}
                  </td>
                  <td className="px-3 py-3">
                    <CapBudgetBar current={key.current_month_cost_usd} cap={key.monthly_cost_cap_usd} locale={locale} />
                  </td>
                  <td className="px-3 py-3">
                    {revoked ? (
                      <span className="rounded-full border border-line bg-surface-sunken px-2 py-0.5 text-label text-ink-muted">
                        {t('status.revoked')}
                      </span>
                    ) : (
                      <span className="rounded-full border border-jade/30 bg-jade/5 px-2 py-0.5 text-label text-jade">
                        {t('status.active')}
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-3 text-end">
                    {!revoked && (
                      <div className="inline-flex gap-1">
                        <button
                          type="button"
                          onClick={() => onConfigure(key)}
                          aria-label={t('actions.configure', { name: key.name })}
                          className="rounded-md p-1 text-ink-secondary transition-colors duration-state ease-he hover:bg-surface-sunken hover:text-ink"
                        >
                          <Settings className="h-4 w-4" aria-hidden="true" />
                        </button>
                        <button
                          type="button"
                          onClick={() => onRevoke(key)}
                          aria-label={t('actions.revoke', { name: key.name })}
                          className="rounded-md p-1 text-crimson transition-colors duration-state ease-he hover:bg-crimson/5"
                        >
                          <Ban className="h-4 w-4" aria-hidden="true" />
                        </button>
                      </div>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {/* Mobile */}
      <div className="space-y-3 md:hidden">
        {keys.map((key) => {
          const revoked = key.revoked_at !== null;
          const scope = readScope(key.scope);
          return (
            <article
              key={key.api_key_id}
              role="region"
              aria-label={t('row.aria', { name: key.name })}
              className={cn(
                'space-y-2 rounded-lg border border-line bg-surface p-3 text-small',
                revoked && 'opacity-50',
              )}
            >
              <div className="flex items-center justify-between">
                <span className="font-medium text-ink" title={key.name}>
                  {key.name}
                </span>
                <code className="tabular text-ink-secondary">{key.key_prefix}…</code>
              </div>
              <ScopeChips scope={scope} onConfigure={revoked ? undefined : () => onConfigure(key)} />
              <div className="flex justify-between text-label text-ink-muted">
                <span>
                  {t('table.header.created')}: <span className="tabular">{formatDate(key.created_at, locale)}</span>
                </span>
                <span>
                  {t('table.header.last_used')}:{' '}
                  <span className="tabular">
                    {key.last_used_at ? formatDate(key.last_used_at, locale) : t('last_used.never')}
                  </span>
                </span>
              </div>
              <CapBudgetBar current={key.current_month_cost_usd} cap={key.monthly_cost_cap_usd} locale={locale} />
              {revoked ? (
                <span className="inline-block rounded-full border border-line bg-surface-sunken px-2 py-0.5 text-label text-ink-muted">
                  {t('status.revoked')}
                </span>
              ) : (
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => onConfigure(key)}
                    aria-label={t('actions.configure', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded-md border border-line-strong px-2 py-1 text-label text-ink-secondary transition-colors duration-state ease-he hover:border-ink-muted hover:text-ink"
                  >
                    <Settings className="h-4 w-4" aria-hidden="true" />
                    {t('table.header.scope')}
                  </button>
                  <button
                    type="button"
                    onClick={() => onRevoke(key)}
                    aria-label={t('actions.revoke', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded-md border border-crimson/40 px-2 py-1 text-label text-crimson transition-colors duration-state ease-he hover:bg-crimson/5"
                  >
                    <Ban className="h-4 w-4" aria-hidden="true" />
                    {t('revoke.confirm.cta')}
                  </button>
                </div>
              )}
            </article>
          );
        })}
      </div>
    </>
  );
}
