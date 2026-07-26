'use client';

/**
 * Story 9.2 AC2 — the request-logs table. Semantic <table> with <th scope="col">
 * (BR-UI / a11y); 8 columns (Time/Model/Status/Streaming/Tokens/Latency/API Key/
 * Request ID) and intentionally NO cost/消费 column (cost is non-authoritative —
 * BR-UI-4 / H-1-R). Status is conveyed by text + icon, never colour alone
 * (WCAG 1.4.1, BR-UI-6). Counts/latency use Intl.NumberFormat (locale digit
 * script). Code-like cells (timestamp, request id, api key, model, numbers)
 * render dir="ltr" so they stay left-to-right even under RTL (BR-UI-7). When the
 * page is empty (total_count=0) an EmptyState with a reset-filters CTA renders
 * instead (BR-UI / BOUNDARY-001).
 */

import { useTranslations } from 'next-intl';

import { type LogEntry, type LogStatusClass, type UsageLogsPage } from '@/lib/api/me-usage';
import { LtrText } from '@/components/business/LtrText';
import { openSupportWithRequestId } from '@/lib/intercom/messenger';
import { Badge, EmptyState, Panel } from '@/components/ui/kit';

export interface RequestLogsTableProps {
  page: UsageLogsPage;
  locale: string;
  /** Href the empty-state "reset filters" CTA points at (the bare /logs route). */
  resetHref: string;
}

/** Map an HTTP status code to its canonical class (BR-RD-6, mirrors the gateway). */
export function logStatusClass(code: number): LogStatusClass {
  if (code >= 500) return 'server_error';
  if (code >= 400) return 'client_error';
  return 'success';
}

const STATUS_ICON: Record<LogStatusClass, string> = {
  success: '✓',
  client_error: '⚠',
  server_error: '✕',
};

// Status class → kit Badge tone (2xx→jade / 4xx→ochre / 5xx→crimson), always
// paired with the icon+text above (never colour-alone) — design-system.md.
const STATUS_TONE: Record<LogStatusClass, 'success' | 'warning' | 'error'> = {
  success: 'success',
  client_error: 'warning',
  server_error: 'error',
};

function formatNumber(n: number, locale: string): string {
  try {
    return new Intl.NumberFormat(locale).format(n);
  } catch {
    return String(n);
  }
}

function formatTimestamp(iso: string, locale: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  try {
    return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'medium' }).format(d);
  } catch {
    return d.toISOString();
  }
}

export function RequestLogsTable({ page, locale, resetHref }: RequestLogsTableProps) {
  const t = useTranslations('logs');

  if (page.total_count === 0 || page.items.length === 0) {
    return (
      <div data-testid="request-logs-empty">
        <EmptyState
          title={t('empty.title')}
          action={
            <a
              href={resetHref}
              className="rounded-md border border-line-strong bg-surface px-3 py-1.5 text-small font-medium text-ink transition-colors duration-state ease-he hover:border-ink-muted"
            >
              {t('empty.reset')}
            </a>
          }
        />
      </div>
    );
  }

  return (
    <Panel padded={false} className="overflow-x-auto">
      <table className="w-full text-small">
        <caption className="sr-only">{t('table.caption')}</caption>
        <thead className="border-b border-line bg-surface-sunken text-start">
          {/* Numeric readout columns (time/tokens/latency) right-align via the
              logical `text-end` (RTL-safe) — 数字右对齐可比 (Iron Law 1). */}
          <tr>
            <th scope="col" className="px-3 py-2 text-end text-label font-medium text-ink-muted">{t('table.columns.time')}</th>
            <th scope="col" className="px-3 py-2 text-start text-label font-medium text-ink-muted">{t('table.columns.model')}</th>
            <th scope="col" className="px-3 py-2 text-start text-label font-medium text-ink-muted">{t('table.columns.status')}</th>
            <th scope="col" className="px-3 py-2 text-start text-label font-medium text-ink-muted">{t('table.columns.streaming')}</th>
            <th scope="col" className="px-3 py-2 text-end text-label font-medium text-ink-muted">{t('table.columns.tokens')}</th>
            <th scope="col" className="px-3 py-2 text-end text-label font-medium text-ink-muted">{t('table.columns.latency')}</th>
            <th scope="col" className="px-3 py-2 text-start text-label font-medium text-ink-muted">{t('table.columns.apiKey')}</th>
            <th scope="col" className="px-3 py-2 text-start text-label font-medium text-ink-muted">{t('table.columns.requestId')}</th>
          </tr>
        </thead>
        <tbody>
          {page.items.map((row) => (
            <LogRow key={row.he_request_id} row={row} locale={locale} t={t} />
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

interface LogRowProps {
  row: LogEntry;
  locale: string;
  t: ReturnType<typeof useTranslations<'logs'>>;
}

function LogRow({ row, locale, t }: LogRowProps) {
  const cls = logStatusClass(row.status_code);
  const tSupport = useTranslations('support');
  return (
    <tr className="border-b border-line last:border-0">
      {/* LTR islands (BR-10.1.8) via the reusable <LtrText> primitive — these
          bidi-neutral values stay left-to-right even under an ar (RTL) document.
          Timestamps/tokens/latency are numeric readouts and model id is a
          technical value — all `tabular` (Iron Law 1). */}
      <LtrText as="td" className="tabular whitespace-nowrap px-3 py-2.5 text-end text-ink">
        {formatTimestamp(row.ts, locale)}
      </LtrText>
      <LtrText as="td" className="tabular px-3 py-2.5 text-ink">{row.model}</LtrText>
      <td className="px-3 py-2.5">
        {/* Status class → kit Badge (2xx 绿/4xx 黄/5xx 红); text + icon inside,
            NOT colour-only (WCAG 1.4.1); the numeric code is a readout → tabular. */}
        <Badge tone={STATUS_TONE[cls]} className="gap-1 whitespace-nowrap">
          <span className="inline-flex items-center gap-1" data-status={cls}>
            <span aria-hidden="true">{STATUS_ICON[cls]}</span>
            <span>{t(`status.${cls}`)}</span>
            <span className="tabular" dir="ltr">{row.status_code}</span>
          </span>
        </Badge>
      </td>
      <td className="px-3 py-2.5 text-ink-secondary">
        {row.is_streaming ? t('table.streaming.yes') : t('table.streaming.no')}
      </td>
      <LtrText as="td" className="tabular px-3 py-2.5 text-end text-ink">{formatNumber(row.total_tokens, locale)}</LtrText>
      <LtrText as="td" className="tabular whitespace-nowrap px-3 py-2.5 text-end text-ink">
        {formatNumber(row.latency_ms_total, locale)} {t('table.latencyUnit')}
      </LtrText>
      <td className="px-3 py-2.5">
        <code className="tabular text-small text-ink-secondary" dir="ltr" title={row.api_key_id}>{row.api_key_id}</code>
      </td>
      <td className="px-3 py-2.5">
        <code className="tabular text-small text-ink-secondary" dir="ltr">{row.he_request_id}</code>
        {/* Story 10.7 AC2 (BR-10.7.9 / front-end-spec:537) — one-click brings
            the non-PII he_request_id handle into the customer-support session.
            Degrades silently if the Messenger isn't booted (region-gated / blocked). */}
        <button
          type="button"
          className="ms-2 text-label text-ink-secondary underline transition-colors duration-state ease-he hover:text-ink"
          aria-label={tSupport('logEntry.ariaLabel', { requestId: row.he_request_id })}
          onClick={() => openSupportWithRequestId(row.he_request_id)}
        >
          {tSupport('logEntry.action')}
        </button>
      </td>
    </tr>
  );
}
