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
    <tr className="border-b text-start text-xs font-medium uppercase tracking-wide text-neutral-500">
      <th className="px-3 py-2">{t('table.header.name')}</th>
      <th className="px-3 py-2">{t('table.header.prefix')}</th>
      <th className="px-3 py-2">{t('table.header.scope')}</th>
      <th className="px-3 py-2">{t('table.header.created')}</th>
      <th className="px-3 py-2">{t('table.header.last_used')}</th>
      <th className="px-3 py-2">{t('table.header.cap')}</th>
      <th className="px-3 py-2">{t('table.header.status')}</th>
      <th className="px-3 py-2 text-end">{t('table.header.actions')}</th>
    </tr>
  );

  return (
    <>
      {/* Desktop */}
      <table className="hidden w-full border-collapse text-sm md:table">
        <thead className="sticky top-0 bg-white">{headers}</thead>
        <tbody>
          {keys.map((key) => {
            const revoked = key.revoked_at !== null;
            const scope = readScope(key.scope);
            return (
              <tr key={key.api_key_id} className={cn('border-b', revoked && 'opacity-50')}>
                <td className="px-3 py-2">
                  <span title={key.name}>{key.name}</span>
                </td>
                <td className="px-3 py-2">
                  <code className="font-mono text-xs">{key.key_prefix}…</code>
                </td>
                <td className="px-3 py-2">
                  <ScopeChips scope={scope} onConfigure={revoked ? undefined : () => onConfigure(key)} />
                </td>
                <td className="px-3 py-2">
                  <time dateTime={key.created_at}>{formatDate(key.created_at, locale)}</time>
                </td>
                <td className="px-3 py-2">
                  {key.last_used_at ? (
                    <time dateTime={key.last_used_at}>{formatDate(key.last_used_at, locale)}</time>
                  ) : (
                    t('last_used.never')
                  )}
                </td>
                <td className="px-3 py-2">
                  <CapBudgetBar current={key.current_month_cost_usd} cap={key.monthly_cost_cap_usd} locale={locale} />
                </td>
                <td className="px-3 py-2">
                  {revoked ? (
                    <span className="rounded-full bg-neutral-200 px-2 py-0.5 text-xs text-neutral-600">
                      {t('status.revoked')}
                    </span>
                  ) : (
                    <span className="rounded-full bg-green-100 px-2 py-0.5 text-xs text-green-700">
                      {t('status.active')}
                    </span>
                  )}
                </td>
                <td className="px-3 py-2 text-end">
                  {!revoked && (
                    <div className="inline-flex gap-1">
                      <button
                        type="button"
                        onClick={() => onConfigure(key)}
                        aria-label={t('actions.configure', { name: key.name })}
                        className="rounded p-1 hover:bg-neutral-100"
                      >
                        <Settings className="h-4 w-4" aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        onClick={() => onRevoke(key)}
                        aria-label={t('actions.revoke', { name: key.name })}
                        className="rounded p-1 text-red-600 hover:bg-red-50"
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
              className={cn('space-y-2 rounded-lg border p-3 text-sm', revoked && 'opacity-50')}
            >
              <div className="flex items-center justify-between">
                <span className="font-medium" title={key.name}>
                  {key.name}
                </span>
                <code className="font-mono text-xs">{key.key_prefix}…</code>
              </div>
              <ScopeChips scope={scope} onConfigure={revoked ? undefined : () => onConfigure(key)} />
              <div className="flex justify-between text-xs text-neutral-600">
                <span>
                  {t('table.header.created')}: {formatDate(key.created_at, locale)}
                </span>
                <span>
                  {t('table.header.last_used')}:{' '}
                  {key.last_used_at ? formatDate(key.last_used_at, locale) : t('last_used.never')}
                </span>
              </div>
              <CapBudgetBar current={key.current_month_cost_usd} cap={key.monthly_cost_cap_usd} locale={locale} />
              {revoked ? (
                <span className="inline-block rounded-full bg-neutral-200 px-2 py-0.5 text-xs text-neutral-600">
                  {t('status.revoked')}
                </span>
              ) : (
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => onConfigure(key)}
                    aria-label={t('actions.configure', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded border px-2 py-1 text-xs hover:bg-neutral-100"
                  >
                    <Settings className="h-4 w-4" aria-hidden="true" />
                    {t('table.header.scope')}
                  </button>
                  <button
                    type="button"
                    onClick={() => onRevoke(key)}
                    aria-label={t('actions.revoke', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded border border-red-200 px-2 py-1 text-xs text-red-600 hover:bg-red-50"
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
