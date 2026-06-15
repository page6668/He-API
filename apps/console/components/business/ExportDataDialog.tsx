'use client';

// Story 2.6 AC1 — Export Data dialog + CTA (client component).
//
// Renders the primary "Export My Data" button + confirmation dialog
// enumerating the 6 data categories per front-end-spec §3.5 +
// security.md §8.3.
//
// State machine:
//   - idle              → CTA enabled (or disabled if currentExport exists)
//   - dialog-open       → confirmation dialog open
//   - submitting        → Server Action in flight
//   - error             → toast displayed; dialog stays open
//
// Accessibility (BR-1.9): focus trap on dialog open, document-level ESC
// closes, focus restored to CTA on close, aria-labelledby points at the
// dialog title. RTL handled by next-intl's RTLProvider (Story 2.1).

import { forwardRef, useEffect, useRef, useState, useTransition, type ReactNode } from 'react';
import { useTranslations, useLocale } from 'next-intl';

import { requestDataExport } from '@/app/[locale]/(console)/settings/data/_actions/request-export';
import type { CurrentExport } from '@/app/[locale]/(console)/settings/data/_actions/get-current-export';

interface ExportDataDialogProps {
  currentExport: CurrentExport;
}

type BannerVariant = 'processing' | 'ready' | 'failed' | null;

function bannerFor(current: CurrentExport): BannerVariant {
  if (!current) return null;
  switch (current.status) {
    case 'pending':
    case 'processing':
      return 'processing';
    case 'completed':
      return current.signed_url_expires_at && new Date(current.signed_url_expires_at) > new Date() ? 'ready' : null;
    case 'failed':
      return 'failed';
    default:
      return null;
  }
}

function isInProgress(current: CurrentExport): boolean {
  if (!current) return false;
  return current.status === 'pending' || current.status === 'processing';
}

export function ExportDataDialog({ currentExport }: ExportDataDialogProps) {
  const t = useTranslations('account.data');
  const locale = useLocale();
  const [open, setOpen] = useState(false);
  const [isPending, startTransition] = useTransition();
  const [toast, setToast] = useState<string | null>(null);
  const ctaRef = useRef<HTMLButtonElement | null>(null);

  const ctaDisabled = isInProgress(currentExport) || isPending;
  const banner = bannerFor(currentExport);

  const onConfirm = () => {
    setToast(null);
    startTransition(async () => {
      const result = await requestDataExport(locale);
      if (result.kind === 'rate_limited') {
        setToast(t('export.errors.rate_limited'));
        return;
      }
      if (result.kind !== 'ok') {
        setToast(t('export.errors.generic'));
        return;
      }
      setOpen(false);
    });
  };

  const closeDialog = () => {
    if (!isPending) setOpen(false);
  };

  return (
    <div className="space-y-4">
      <Button
        ref={ctaRef}
        onClick={() => setOpen(true)}
        disabled={ctaDisabled}
        title={isInProgress(currentExport) ? t('export.cta.disabled_in_progress') : undefined}
        aria-disabled={ctaDisabled}
      >
        {t('export.cta.label')}
      </Button>

      {banner === 'processing' && (
        <Banner variant="info">{t('export.banner.processing')}</Banner>
      )}
      {banner === 'ready' && (
        <Banner variant="success">{t('export.banner.ready')}</Banner>
      )}
      {banner === 'failed' && (
        <Banner variant="error">{t('export.banner.failed')}</Banner>
      )}

      {toast && (
        <Banner variant="error" role="alert">
          {toast}
        </Banner>
      )}

      {open && (
        <Dialog
          titleId="export-data-dialog-title"
          onClose={closeDialog}
          restoreFocusTo={ctaRef}
        >
          <h2 id="export-data-dialog-title" className="text-lg font-semibold">
            {t('export.dialog.title')}
          </h2>
          <p className="mt-2 text-sm text-neutral-700">{t('export.dialog.body_intro')}</p>
          <ul className="mt-3 list-disc space-y-1 ps-6 text-sm">
            <li>{t('export.dialog.categories.users')}</li>
            <li>{t('export.dialog.categories.api_keys')}</li>
            <li>{t('export.dialog.categories.request_logs')}</li>
            <li>{t('export.dialog.categories.orders')}</li>
            <li>{t('export.dialog.categories.balances')}</li>
            <li>{t('export.dialog.categories.safety_logs')}</li>
          </ul>
          <p className="mt-3 text-xs text-neutral-500">{t('export.dialog.slo')}</p>
          <div className="mt-5 flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={isPending}>
              {t('export.dialog.cancel')}
            </Button>
            <Button onClick={onConfirm} disabled={isPending}>
              {t('export.dialog.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

// ---- Inline UI primitives ----
// Local primitives so the file is self-contained; the actual shadcn-ui
// <Button>/<Banner> can be swapped in by a follow-up commit. The Dialog
// below implements WCAG 2.1 AA focus management (BR-1.9) directly rather
// than waiting for the shadcn-ui chrome to land — focus trap, restoration
// to a caller-supplied trigger, and a document-level ESC handler.

interface ButtonProps {
  onClick?: () => void;
  disabled?: boolean;
  variant?: 'default' | 'ghost';
  children: ReactNode;
  title?: string;
  'aria-disabled'?: boolean;
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { onClick, disabled, variant = 'default', children, title, ...rest },
  ref,
) {
  const base = 'inline-flex items-center rounded px-3 py-1.5 text-sm transition';
  const variants = {
    default: 'bg-blue-600 text-white hover:bg-blue-700 disabled:bg-neutral-300 disabled:text-neutral-500',
    ghost: 'bg-transparent text-neutral-700 hover:bg-neutral-100 disabled:text-neutral-400',
  };
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className={`${base} ${variants[variant]}`}
      ref={ref}
      {...rest}
    >
      {children}
    </button>
  );
});

interface BannerProps {
  variant: 'info' | 'success' | 'error';
  children: ReactNode;
  role?: 'alert' | 'status';
}

function Banner({ variant, children, role = 'status' }: BannerProps) {
  const styles = {
    info: 'bg-blue-50 text-blue-900 border-blue-200',
    success: 'bg-green-50 text-green-900 border-green-200',
    error: 'bg-red-50 text-red-900 border-red-200',
  } as const;
  return (
    <div role={role} className={`rounded border px-3 py-2 text-sm ${styles[variant]}`}>
      {children}
    </div>
  );
}

interface DialogProps {
  titleId: string;
  onClose: () => void;
  restoreFocusTo?: React.RefObject<HTMLElement | null>;
  children: ReactNode;
}

// FOCUSABLE_SELECTOR mirrors WCAG-compliant focusable elements; excludes
// negative tabindex and hidden inputs.
const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

function Dialog({ titleId, onClose, restoreFocusTo, children }: DialogProps) {
  const contentRef = useRef<HTMLDivElement | null>(null);

  // BR-1.9: focus the first focusable child on open + remember the
  // previously-focused element so we can restore focus on close.
  useEffect(() => {
    const previouslyFocused = (document.activeElement as HTMLElement | null) ?? null;
    const root = contentRef.current;
    if (root) {
      const first = root.querySelector<HTMLElement>(FOCUSABLE_SELECTOR);
      (first ?? root).focus();
    }
    return () => {
      const target = restoreFocusTo?.current ?? previouslyFocused;
      // requestAnimationFrame defers focus to after React unmount commit so
      // the browser doesn't drop focus on `document.body`.
      requestAnimationFrame(() => target?.focus());
    };
  }, [restoreFocusTo]);

  // BR-1.9: document-level ESC + focus-trap on Tab/Shift+Tab. Document
  // scope is required so ESC fires regardless of which focusable inside
  // the dialog currently has focus (or even if focus has somehow
  // escaped). Trap cycles focus inside the dialog content.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const root = contentRef.current;
      if (!root) return;
      const focusables = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
        (el) => !el.hasAttribute('disabled') && el.tabIndex !== -1,
      );
      if (focusables.length === 0) {
        e.preventDefault();
        return;
      }
      const first = focusables[0]!;
      const last = focusables[focusables.length - 1]!;
      const active = document.activeElement as HTMLElement | null;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/30"
      onClick={(e) => {
        // Click-on-overlay closes; clicks inside the content stop here.
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div ref={contentRef} tabIndex={-1} className="w-full max-w-md rounded bg-white p-5 shadow-lg outline-none">
        {children}
      </div>
    </div>
  );
}
