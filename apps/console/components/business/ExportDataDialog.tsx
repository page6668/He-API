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

import { useEffect, useRef, useState, useTransition, type ReactNode } from 'react';
import { useTranslations, useLocale } from 'next-intl';

import { requestDataExport } from '@/app/[locale]/(console)/settings/data/_actions/request-export';
import type { CurrentExport } from '@/app/[locale]/(console)/settings/data/_actions/get-current-export';
import { Button, Notice, Panel } from '@/components/ui/kit';

// kit Button 不 forwardRef(React 18),CTA 需要 ref 做焦点恢复(BR-1.9)——
// 与 DeleteAccountDialog 相同的手写 native button 习语,类名与 kit secondary 完全一致。
// 导出=次级操作(描边,design-ui M6-D);朱砂只落在确认对话框内的 Confirm(每屏一枚印)。
const secondaryBtnCls =
  'inline-flex items-center rounded-md border border-line-strong bg-surface px-4 py-2 text-small font-medium text-ink transition-colors duration-state ease-he hover:border-ink-muted hover:bg-surface-sunken focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30 focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50';

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
    // 导出区卡片化 —— 与下方 Danger Zone 的 Panel 节奏对齐(1px 暖边框、p-6、零阴影)。
    <Panel className="space-y-4">
      <button
        type="button"
        ref={ctaRef}
        onClick={() => setOpen(true)}
        disabled={ctaDisabled}
        title={isInProgress(currentExport) ? t('export.cta.disabled_in_progress') : undefined}
        aria-disabled={ctaDisabled}
        className={secondaryBtnCls}
      >
        {t('export.cta.label')}
      </button>

      {banner === 'processing' && (
        <Notice tone="neutral" role="status">
          {t('export.banner.processing')}
        </Notice>
      )}
      {banner === 'ready' && (
        // 成功读数:安静的一行竹绿文字(与 ProfileForm 保存成功同款,不做满宽绿底)。
        <div
          role="status"
          className="rounded-lg border border-line bg-surface px-4 py-2.5 text-small text-jade"
        >
          {t('export.banner.ready')}
        </div>
      )}
      {banner === 'failed' && (
        <Notice tone="error" role="status">
          {t('export.banner.failed')}
        </Notice>
      )}

      {toast && (
        <Notice tone="error" role="alert">
          {toast}
        </Notice>
      )}

      {open && (
        <Dialog
          titleId="export-data-dialog-title"
          onClose={closeDialog}
          restoreFocusTo={ctaRef}
        >
          <h2 id="export-data-dialog-title" className="text-h3 text-ink">
            {t('export.dialog.title')}
          </h2>
          <p className="mt-2 text-small text-ink-secondary">{t('export.dialog.body_intro')}</p>
          <ul className="mt-3 list-disc space-y-1 ps-6 text-small text-ink-secondary">
            <li>{t('export.dialog.categories.users')}</li>
            <li>{t('export.dialog.categories.api_keys')}</li>
            <li>{t('export.dialog.categories.request_logs')}</li>
            <li>{t('export.dialog.categories.orders')}</li>
            <li>{t('export.dialog.categories.balances')}</li>
            <li>{t('export.dialog.categories.safety_logs')}</li>
          </ul>
          <p className="mt-3 text-label text-ink-muted">{t('export.dialog.slo')}</p>
          <div className="mt-5 flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={isPending}>
              {t('export.dialog.cancel')}
            </Button>
            {/* 对话框内唯一的朱砂 —— 确认导出是这一屏(浮层)的主操作(铁律2)。 */}
            <Button variant="primary" onClick={onConfirm} disabled={isPending}>
              {t('export.dialog.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </Panel>
  );
}

// ---- Inline Dialog primitive ----
// Button/Notice/Panel 已换用 kit 共享基元;Dialog 保留本地实现 —— 它直接实现了
// WCAG 2.1 AA 焦点管理(BR-1.9):focus trap、恢复到调用方提供的触发器、
// document 级 ESC 处理。面板样式与 kit Panel 对齐(1px 暖边框、零阴影,
// 仅浮层允许 shadow-overlay)。

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
      className="fixed inset-0 z-50 flex items-center justify-center bg-ink/20"
      onClick={(e) => {
        // Click-on-overlay closes; clicks inside the content stop here.
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={contentRef}
        tabIndex={-1}
        className="w-full max-w-md rounded-lg border border-line bg-surface p-6 shadow-overlay outline-none"
      >
        {children}
      </div>
    </div>
  );
}
