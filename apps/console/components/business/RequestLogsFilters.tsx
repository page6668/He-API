'use client';

/**
 * Story 9.2 AC2 — the request-logs filter bar. A labelled <form> (a11y, BR-UI-1)
 * over Model / Status / Streaming / time range / page-size. Applying re-queries
 * via a URL navigation (BR-UI-3 — filters live in the query string so the view is
 * shareable / back-button-safe) and ALWAYS resets to offset 0 (BR-UI / FLOW-005).
 * The time range is validated client-side (end > start) — an invalid range shows
 * an inline error and the query is NOT fired (BR-UI Data Validation).
 */

import { useState } from 'react';
import { usePathname, useRouter, useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';

import { LOG_LIMIT_OPTIONS, LOG_STATUS_CLASSES, LOG_DEFAULT_LIMIT } from '@/lib/api/me-usage';
import { Button, Panel, labelCls } from '@/components/ui/kit';

// Same token language as kit's fieldCls (color/radius/focus ring) but WITHOUT
// its `w-full` — fieldCls is tuned for a stacked full-width column (Playground);
// this is an inline filter bar where each control should size to its content.
const filterFieldCls =
  'rounded-md border border-line-strong bg-surface px-2.5 py-1.5 text-small text-ink outline-none transition-colors duration-state ease-he focus:border-seal focus:ring-2 focus:ring-seal/15';

/** RFC3339 (with offset/Z) → the value a <input type="datetime-local"> expects. */
function toLocalInput(rfc3339: string): string {
  const d = new Date(rfc3339);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** datetime-local value → canonical RFC3339 (UTC) for the gateway. */
function toRFC3339(local: string): string {
  if (!local) return '';
  const d = new Date(local);
  if (Number.isNaN(d.getTime())) return '';
  return d.toISOString();
}

export function RequestLogsFilters() {
  const t = useTranslations('logs');
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const [model, setModel] = useState(searchParams.get('model') ?? '');
  const [status, setStatus] = useState(searchParams.get('status') ?? '');
  const [streaming, setStreaming] = useState(searchParams.get('is_streaming') ?? '');
  const [start, setStart] = useState(toLocalInput(searchParams.get('start') ?? ''));
  const [end, setEnd] = useState(toLocalInput(searchParams.get('end') ?? ''));
  const [limit, setLimit] = useState(searchParams.get('limit') ?? String(LOG_DEFAULT_LIMIT));
  const [rangeError, setRangeError] = useState(false);

  function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    // end must be strictly after start when both are present — otherwise inline
    // error, query NOT fired (BR-UI Data Validation / 9.2-UNIT-022).
    if (start && end && new Date(end).getTime() <= new Date(start).getTime()) {
      setRangeError(true);
      return;
    }
    setRangeError(false);

    const params = new URLSearchParams();
    const m = model.trim();
    if (m) params.set('model', m);
    if (status) params.set('status', status);
    if (streaming) params.set('is_streaming', streaming);
    const s = toRFC3339(start);
    const en = toRFC3339(end);
    if (s) params.set('start', s);
    if (en) params.set('end', en);
    if (limit && limit !== String(LOG_DEFAULT_LIMIT)) params.set('limit', limit);
    // offset intentionally omitted → resets to 0 on any filter change (FLOW-005).

    const qs = params.toString();
    router.push(qs ? `${pathname}?${qs}` : pathname);
  }

  function onReset() {
    setModel('');
    setStatus('');
    setStreaming('');
    setStart('');
    setEnd('');
    setLimit(String(LOG_DEFAULT_LIMIT));
    setRangeError(false);
    router.push(pathname);
  }

  return (
    <Panel>
      <form
        onSubmit={onSubmit}
        aria-label={t('filters.legend')}
        className="flex flex-wrap items-end gap-4"
      >
        <Field label={t('filters.model.label')} htmlFor="logs-model">
          {/* Model id is a technical value — tabular (Plex Mono), per Iron Law 1. */}
          <input
            id="logs-model"
            type="text"
            value={model}
            maxLength={128}
            onChange={(e) => setModel(e.target.value)}
            placeholder={t('filters.model.placeholder')}
            className={`${filterFieldCls} tabular`}
          />
        </Field>

        <Field label={t('filters.status.label')} htmlFor="logs-status">
          <select
            id="logs-status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className={filterFieldCls}
          >
            <option value="">{t('filters.status.all')}</option>
            {LOG_STATUS_CLASSES.map((c) => (
              <option key={c} value={c}>{t(`status.${c}`)}</option>
            ))}
          </select>
        </Field>

        <Field label={t('filters.streaming.label')} htmlFor="logs-streaming">
          <select
            id="logs-streaming"
            value={streaming}
            onChange={(e) => setStreaming(e.target.value)}
            className={filterFieldCls}
          >
            <option value="">{t('filters.streaming.all')}</option>
            <option value="true">{t('filters.streaming.yes')}</option>
            <option value="false">{t('filters.streaming.no')}</option>
          </select>
        </Field>

        <Field label={t('filters.start.label')} htmlFor="logs-start">
          <input
            id="logs-start"
            type="datetime-local"
            value={start}
            onChange={(e) => setStart(e.target.value)}
            className={`${filterFieldCls} tabular`}
          />
        </Field>

        <Field label={t('filters.end.label')} htmlFor="logs-end">
          <input
            id="logs-end"
            type="datetime-local"
            value={end}
            aria-invalid={rangeError || undefined}
            aria-describedby={rangeError ? 'logs-range-error' : undefined}
            onChange={(e) => setEnd(e.target.value)}
            className={`${filterFieldCls} tabular`}
          />
        </Field>

        <Field label={t('filters.limit.label')} htmlFor="logs-limit">
          <select
            id="logs-limit"
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
            className={`${filterFieldCls} tabular`}
          >
            {LOG_LIMIT_OPTIONS.map((n) => (
              <option key={n} value={String(n)}>{n}</option>
            ))}
          </select>
        </Field>

        <div className="flex gap-2">
          {/* Filter-bar affirmative action uses INK, never seal (that's reserved
              for the export CTA elsewhere on this screen — Iron Law 2). */}
          <button
            type="submit"
            className="rounded-md bg-ink px-4 py-2 text-small font-medium text-paper transition-colors duration-state ease-he hover:bg-ink/90 focus:outline-none focus:ring-2 focus:ring-ink/20"
          >
            {t('filters.apply')}
          </button>
          <Button type="button" variant="secondary" onClick={onReset}>
            {t('filters.reset')}
          </Button>
        </div>

        {rangeError && (
          <p id="logs-range-error" role="alert" className="w-full text-small text-crimson">
            {t('filters.errors.range')}
          </p>
        )}
      </form>
    </Panel>
  );
}

function Field({ label, htmlFor, children }: { label: string; htmlFor: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={htmlFor} className={labelCls}>{label}</label>
      {children}
    </div>
  );
}
