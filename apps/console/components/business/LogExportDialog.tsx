'use client';

// Story 9.3 AC3 — usage-log export control on /{locale}/logs.
//
// Mirrors the 2.6 ExportDataDialog interaction (format picker + CTA-disable +
// status/banner) but targets the NEW /v1/me/usage/logs/export family and adds a
// JSON/CSV format picker. The signed download link is delivered by EMAIL only —
// this control NEVER renders a raw URL (BR-UI-3); the response carries only the
// expiry timestamp.
//
// a11y (BR-UI-5, WCAG 2.1 AA): labeled <form>, format radiogroup, submit button
// with aria + disabled states, status conveyed by text+icon (never color alone).
// RTL (ar): layout mirrors; format codes (JSON/CSV) + timestamps stay LTR.

import { useRef, useState, useTransition } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/kit';
import {
  requestLogExport,
  type LogExportFormat,
  type RequestLogExportResult,
} from '@/app/[locale]/(console)/logs/_actions/request-log-export';
import type { CurrentLogExport } from '@/app/[locale]/(console)/logs/_actions/get-current-log-export';

interface LogExportDialogProps {
  locale: string;
  current: CurrentLogExport;
}

type Notice = 'rate_limited' | 'error' | null;

function isInProgress(c: CurrentLogExport): boolean {
  return c?.status === 'pending' || c?.status === 'processing';
}

function isCompletedLive(c: CurrentLogExport): boolean {
  if (!c || c.status !== 'completed' || !c.signed_url_expires_at) return false;
  const exp = Date.parse(c.signed_url_expires_at);
  return Number.isFinite(exp) && exp > Date.now();
}

export function LogExportDialog({ locale, current }: LogExportDialogProps) {
  const t = useTranslations('logs');
  const router = useRouter();
  const [format, setFormat] = useState<LogExportFormat>('json');
  const [pending, startTransition] = useTransition();
  // Optimistic in-progress after a successful submit (before revalidate lands).
  const [submitted, setSubmitted] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  // Synchronous in-flight guard so two clicks in the same tick fire ONE action
  // (BLIND-FLOW-001 — the React `disabled` state only flips on the next render).
  const inFlight = useRef(false);

  const inProgress = submitted || isInProgress(current);
  const disabled = pending || inProgress;

  function handleResult(res: RequestLogExportResult) {
    inFlight.current = false;
    switch (res.kind) {
      case 'ok':
        setSubmitted(true);
        setNotice(null);
        break;
      case 'unauthorized':
        router.push(`/${locale}/signin?return_to=/${locale}/logs`);
        break;
      case 'rate_limited':
        setNotice('rate_limited');
        break;
      case 'error':
      default:
        setNotice('error');
        break;
    }
  }

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (disabled || inFlight.current) return; // BLIND-FLOW-001 — no double submit
    inFlight.current = true;
    setNotice(null);
    startTransition(() => {
      requestLogExport(format, locale)
        .then(handleResult)
        .catch(() => {
          inFlight.current = false;
          setNotice('error');
        });
    });
  }

  return (
    // Compact card sized to live in PageShell's header `actions` slot (kit.tsx) —
    // the single main action on /logs. Panel-equivalent tokens (1px warm border,
    // white surface, zero shadow); no literal <Panel> import since this needs a
    // <section> tag to keep its aria-labelledby wiring.
    <section
      aria-labelledby="log-export-heading"
      className="w-full rounded-lg border border-line bg-surface p-4 sm:w-80"
    >
      <h2 id="log-export-heading" className="text-h3">
        {t('export.heading')}
      </h2>
      <p className="mt-1 text-small text-ink-secondary">{t('export.description')}</p>

      <form onSubmit={onSubmit} className="mt-4 space-y-4">
        <fieldset>
          <legend className="text-label text-ink-secondary">{t('export.format.legend')}</legend>
          <div role="radiogroup" aria-label={t('export.format.legend')} className="mt-2 flex gap-4">
            {(['json', 'csv'] as const).map((f) => (
              <label key={f} className="inline-flex items-center gap-2 text-small text-ink">
                <input
                  type="radio"
                  name="log-export-format"
                  value={f}
                  checked={format === f}
                  onChange={() => setFormat(f)}
                  disabled={disabled}
                  className="h-4 w-4 border-line-strong text-ink accent-ink"
                />
                {/* format codes are universal — literal + LTR even under RTL */}
                <span dir="ltr" className="tabular">{f.toUpperCase()}</span>
              </label>
            ))}
          </div>
        </fieldset>

        {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2) */}
        <Button
          type="submit"
          variant="primary"
          disabled={disabled}
          aria-disabled={disabled}
          aria-label={t('export.submit')}
          className="w-full"
        >
          {pending ? t('export.submitting') : t('export.submit')}
        </Button>
      </form>

      {/* Status / banners — text + icon, never color-alone (BR-UI-5). Plain
          typographic glyphs (same convention as RequestLogsTable's status
          icons) — deliberately not an SVG icon set here: this control's DOM
          must never contain a raw URL substring (BR-UI-3 / 9.3-UNIT-040), and
          bundled icon components carry an xmlns="http://…" attribute that
          would trip that check. */}
      {inProgress && (
        <p
          role="status"
          className="mt-4 flex items-center gap-2 rounded-lg border border-line bg-surface-sunken px-3 py-2 text-small text-ink-secondary"
          data-testid="log-export-in-progress"
        >
          <span aria-hidden="true">…</span>
          {t('export.status.in_progress')}
        </p>
      )}

      {!inProgress && isCompletedLive(current) && current && (
        <p
          role="status"
          className="mt-4 flex items-center gap-2 rounded-lg border border-jade/30 bg-jade/5 px-3 py-2 text-small text-jade"
          data-testid="log-export-emailed"
        >
          <span aria-hidden="true">✓</span>
          {t('export.banner.emailed', { expiry: formatExpiry(current.signed_url_expires_at, locale) })}
        </p>
      )}

      {!inProgress && current?.status === 'failed' && (
        <p
          role="status"
          className="mt-4 flex items-center gap-2 rounded-lg border border-crimson/30 bg-crimson/5 px-3 py-2 text-small text-crimson"
          data-testid="log-export-failed"
        >
          <span aria-hidden="true">✕</span>
          {t('export.banner.failed')}
        </p>
      )}

      {notice === 'rate_limited' && (
        <p
          role="alert"
          className="mt-4 flex items-center gap-2 rounded-lg border border-ochre/30 bg-ochre/5 px-3 py-2 text-small text-ochre"
          data-testid="log-export-rate-limited"
        >
          <span aria-hidden="true">⚠</span>
          {t('export.errors.rate_limited')}
        </p>
      )}
      {notice === 'error' && (
        <p
          role="alert"
          className="mt-4 flex items-center gap-2 rounded-lg border border-crimson/30 bg-crimson/5 px-3 py-2 text-small text-crimson"
          data-testid="log-export-error"
        >
          <span aria-hidden="true">✕</span>
          {t('export.errors.generic')}
        </p>
      )}
    </section>
  );
}

// formatExpiry renders the expiry in the user's locale; wrapped LTR by the
// caller's surrounding markup. Falls back to the raw string if unparseable.
function formatExpiry(iso: string | null, locale: string): string {
  if (!iso) return '';
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return iso;
  try {
    return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(ms));
  } catch {
    return iso;
  }
}
