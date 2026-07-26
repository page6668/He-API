'use client';

/**
 * Story 5.5 AC1 — KeysTable (T1.2).
 *
 * M6-A 美化:桌面端由 8 列 <table> 改为密集列表行(对齐 ModelsCatalog 的既定做法:
 * Panel padded={false} + ul.divide-y divide-line,行 py-3.5,hover 只提一档纸色)。
 * 左侧身份块 = 名称 + 等宽前缀 + 状态 Badge(kit 五色:active→success / revoked→neutral)
 * + ScopeChips;右侧读数块 = 创建/最近使用/月上限(text-label 标签 + tabular 值,
 * 次要信息 text-ink-secondary)。行内操作(配置/吊销)一律次级描边 —— 危险的红只落在
 * RevokeKeyDialog 的确认按钮上。移动端(<md)保留卡片堆叠,同步换用 kit Badge。
 * Revoked 行 opacity-50 且无操作按钮(configure/revoke 对终态键是 no-op,BR-L-5)。
 */

import type { ReactNode } from 'react';
import { Settings, Ban } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { cn } from '@/lib/utils';
import { Badge, Panel } from '@/components/ui/kit';
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

/** 行内操作:次级描边(secondary),不落红 —— 红色只属于确认对话框的确认按钮。 */
const rowActionCls =
  'rounded-md border border-line-strong bg-surface p-1.5 text-ink-secondary transition-colors duration-state ease-he hover:border-ink-muted hover:bg-surface-sunken hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30';

/** 读数格:小标签 + tabular 值(对齐 ModelsCatalog.MetricCell,数字右对齐铁律)。 */
function MetricCell({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-[5.5rem] lg:text-end">
      <dt className="text-label text-ink-muted">{label}</dt>
      <dd className="tabular mt-0.5 text-small text-ink-secondary">{value}</dd>
    </div>
  );
}

function StatusBadge({ revoked }: { revoked: boolean }) {
  const t = useTranslations('account.keys');
  return revoked ? (
    <Badge>{t('status.revoked')}</Badge>
  ) : (
    <Badge tone="success">{t('status.active')}</Badge>
  );
}

export function KeysTable({ keys, locale, onConfigure, onRevoke }: KeysTableProps) {
  const t = useTranslations('account.keys');

  return (
    <>
      {/* Desktop — calm-dense 数据行:1px 暖边框分隔,零阴影,hover 只提一档纸色。 */}
      <div className="hidden md:block">
        <Panel padded={false}>
          <ul className="divide-y divide-line">
            {keys.map((key) => {
              const revoked = key.revoked_at !== null;
              const scope = readScope(key.scope);
              return (
                <li
                  key={key.api_key_id}
                  className={cn(
                    'grid gap-x-6 gap-y-2 px-5 py-3.5 transition-colors duration-state ease-he hover:bg-paper lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center',
                    revoked && 'opacity-50',
                  )}
                >
                  {/* 身份块:名称 + 等宽前缀 + 状态徽章;下行 scope chips */}
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
                      <span className="truncate text-small font-medium text-ink" title={key.name}>
                        {key.name}
                      </span>
                      <code className="tabular text-small text-ink-secondary">{key.key_prefix}…</code>
                      <StatusBadge revoked={revoked} />
                    </div>
                    <div className="mt-1.5">
                      <ScopeChips
                        scope={scope}
                        onConfigure={revoked ? undefined : () => onConfigure(key)}
                      />
                    </div>
                  </div>

                  {/* 读数块:创建 / 最近使用 / 月上限,≥lg 右对齐可比 */}
                  <div className="flex flex-wrap items-center gap-x-6 gap-y-1.5 lg:justify-end">
                    <dl className="flex flex-wrap items-baseline gap-x-6 gap-y-1.5">
                      <MetricCell
                        label={t('table.header.created')}
                        value={
                          <time dateTime={key.created_at}>{formatDate(key.created_at, locale)}</time>
                        }
                      />
                      <MetricCell
                        label={t('table.header.last_used')}
                        value={
                          key.last_used_at ? (
                            <time dateTime={key.last_used_at}>
                              {formatDate(key.last_used_at, locale)}
                            </time>
                          ) : (
                            <span className="text-ink-muted">{t('last_used.never')}</span>
                          )
                        }
                      />
                      <div className="min-w-[7rem]">
                        <dt className="text-label text-ink-muted">{t('table.header.cap')}</dt>
                        <dd className="mt-0.5">
                          <CapBudgetBar
                            current={key.current_month_cost_usd}
                            cap={key.monthly_cost_cap_usd}
                            locale={locale}
                          />
                        </dd>
                      </div>
                    </dl>
                    {!revoked && (
                      <div className="inline-flex gap-1.5">
                        <button
                          type="button"
                          onClick={() => onConfigure(key)}
                          aria-label={t('actions.configure', { name: key.name })}
                          className={rowActionCls}
                        >
                          <Settings className="h-4 w-4" aria-hidden="true" />
                        </button>
                        <button
                          type="button"
                          onClick={() => onRevoke(key)}
                          aria-label={t('actions.revoke', { name: key.name })}
                          className={rowActionCls}
                        >
                          <Ban className="h-4 w-4" aria-hidden="true" />
                        </button>
                      </div>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </Panel>
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
              <div className="flex items-center justify-between gap-2">
                <span className="truncate font-medium text-ink" title={key.name}>
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
                <StatusBadge revoked />
              ) : (
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => onConfigure(key)}
                    aria-label={t('actions.configure', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded-md border border-line-strong px-2 py-1 text-label text-ink-secondary transition-colors duration-state ease-he hover:border-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30"
                  >
                    <Settings className="h-4 w-4" aria-hidden="true" />
                    {t('table.header.scope')}
                  </button>
                  {/* 次级描边 —— 红色只落在 RevokeKeyDialog 的确认按钮上 */}
                  <button
                    type="button"
                    onClick={() => onRevoke(key)}
                    aria-label={t('actions.revoke', { name: key.name })}
                    className="inline-flex items-center gap-1 rounded-md border border-line-strong px-2 py-1 text-label text-ink-secondary transition-colors duration-state ease-he hover:border-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30"
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
