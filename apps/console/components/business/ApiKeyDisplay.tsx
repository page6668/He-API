'use client';

/**
 * Story 5.5 AC2 — ApiKeyDisplay (T2.2). FIRST realization per front-end-spec
 * §5.2 line 602.
 *
 * One-time plaintext display: mask by default → reveal toggle → copy → "I've
 * saved it" confirm. Plaintext discipline (BR-PD-1..7): the value lives ONLY in
 * the React prop/state tree + (briefly) the OS clipboard after an explicit
 * copy — NEVER localStorage / sessionStorage / IndexedDB / cookies. On confirm
 * we router.replace('/{locale}/keys'), scrubbing the plaintext-bearing URL from
 * browser history (BR-C-3).
 */

import { useState, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { useTranslations } from 'next-intl';

import { Button, Notice } from '@/components/ui/kit';
import { Dialog } from '@/components/ui/dialog';

export interface ApiKeyDisplayProps {
  plaintext: string;
  apiKeyId: string;
  locale: string;
}

const MASK = `he-${'•'.repeat(43)}`;

export function ApiKeyDisplay({ plaintext, locale }: ApiKeyDisplayProps) {
  const t = useTranslations('account.keys');
  const router = useRouter();
  const [revealed, setRevealed] = useState(false);
  const [announced, setAnnounced] = useState(false);
  const [toast, setToast] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);
  const [confirmClose, setConfirmClose] = useState(false);
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);

  function toggleReveal() {
    setRevealed((r) => !r);
    if (!announced) setAnnounced(true); // one-shot SR announcement (BR-A11Y-3)
  }

  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(plaintext);
      setToast({ kind: 'success', text: t('created.copied') });
    } catch {
      setToast({ kind: 'error', text: t('created.copy_failed') });
    }
  }

  function confirmSaved() {
    router.replace(`/${locale}/keys`);
  }

  return (
    <div className="mx-auto max-w-lg space-y-5">
      {/* 页面标题由外层 PageShell 渲染(created/page.tsx),此处不再重复 <h1>。 */}
      <Notice tone="warning" role="alert">
        {t('created.warning')}
      </Notice>

      <div className="space-y-2">
        <span className="block text-label text-ink-secondary">{t('created.label')}</span>
        <div className="flex items-center gap-2">
          {/* 读出区:内嵌暗底 + 等宽 —— 密钥是"读数",不是正文。 */}
          <code className="tabular flex-1 select-all break-all rounded-md border border-line bg-surface-sunken px-3 py-2 text-small text-ink">
            {revealed ? plaintext : MASK}
          </code>
          <Button type="button" variant="secondary" onClick={toggleReveal} aria-pressed={revealed}>
            {revealed ? t('created.hide') : t('created.reveal')}
          </Button>
          <Button type="button" variant="secondary" onClick={copy}>
            {t('created.copy')}
          </Button>
        </div>
        {/* One-shot screen-reader announcement on first reveal (BR-A11Y-3). */}
        <span className="sr-only" aria-live="polite">
          {announced ? plaintext : ''}
        </span>
      </div>

      <div className="flex items-center justify-between">
        {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2) */}
        <Button type="button" variant="primary" onClick={confirmSaved}>
          {t('created.saved_cta')}
        </Button>
        <button
          type="button"
          ref={closeBtnRef}
          onClick={() => setConfirmClose(true)}
          className="text-small text-ink-secondary underline decoration-line-strong underline-offset-2 transition-colors duration-state ease-he hover:text-ink hover:decoration-ink"
        >
          {t('created.close_without_saving')}
        </button>
      </div>

      {toast && (
        <Notice tone={toast.kind === 'error' ? 'error' : 'neutral'} role={toast.kind === 'error' ? 'alert' : 'status'}>
          {toast.text}
        </Notice>
      )}

      {confirmClose && (
        <Dialog
          titleId="confirm-close-title"
          role="alertdialog"
          closeOnOverlay={false}
          onClose={() => setConfirmClose(false)}
          restoreFocusTo={closeBtnRef}
          className="rounded-lg border border-line bg-surface shadow-overlay"
        >
          <h2 id="confirm-close-title" className="text-h3 text-ink">
            {t('created.confirm_close.title')}
          </h2>
          <p className="mt-2 text-small text-ink-secondary">{t('created.confirm_close.body')}</p>
          <div className="mt-4 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setConfirmClose(false)}>
              {t('created.confirm_close.cancel')}
            </Button>
            <Button type="button" variant="danger" onClick={confirmSaved}>
              {t('created.confirm_close.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
