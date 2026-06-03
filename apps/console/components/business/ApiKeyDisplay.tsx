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

import { Button } from '@/components/ui/button';
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
      <h1 className="text-2xl font-semibold">{t('created.title')}</h1>

      <div role="alert" aria-live="assertive" className="rounded border-2 border-red-400 bg-red-50 p-3 text-sm text-red-900">
        {t('created.warning')}
      </div>

      <div className="space-y-2">
        <span className="block text-sm font-medium">{t('created.label')}</span>
        <div className="flex items-center gap-2">
          <code className="flex-1 break-all rounded bg-neutral-100 px-3 py-2 font-mono text-sm select-all">
            {revealed ? plaintext : MASK}
          </code>
          <Button type="button" variant="outline" onClick={toggleReveal} aria-pressed={revealed}>
            {revealed ? t('created.hide') : t('created.reveal')}
          </Button>
          <Button type="button" variant="outline" onClick={copy}>
            {t('created.copy')}
          </Button>
        </div>
        {/* One-shot screen-reader announcement on first reveal (BR-A11Y-3). */}
        <span className="sr-only" aria-live="polite">
          {announced ? plaintext : ''}
        </span>
      </div>

      <div className="flex items-center justify-between">
        <Button type="button" onClick={confirmSaved}>
          {t('created.saved_cta')}
        </Button>
        <button
          type="button"
          ref={closeBtnRef}
          onClick={() => setConfirmClose(true)}
          className="text-sm text-neutral-500 underline"
        >
          {t('created.close_without_saving')}
        </button>
      </div>

      {toast && (
        <p
          role={toast.kind === 'error' ? 'alert' : 'status'}
          aria-live={toast.kind === 'error' ? 'assertive' : 'polite'}
          className="text-sm text-neutral-700"
        >
          {toast.text}
        </p>
      )}

      {confirmClose && (
        <Dialog
          titleId="confirm-close-title"
          role="alertdialog"
          closeOnOverlay={false}
          onClose={() => setConfirmClose(false)}
          restoreFocusTo={closeBtnRef}
        >
          <h2 id="confirm-close-title" className="text-lg font-semibold">
            {t('created.confirm_close.title')}
          </h2>
          <p className="mt-2 text-sm text-neutral-700">{t('created.confirm_close.body')}</p>
          <div className="mt-4 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setConfirmClose(false)}>
              {t('created.confirm_close.cancel')}
            </Button>
            <Button type="button" variant="destructive" onClick={confirmSaved}>
              {t('created.confirm_close.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
