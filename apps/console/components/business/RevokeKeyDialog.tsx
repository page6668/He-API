'use client';

/**
 * Story 5.5 AC4 — RevokeKeyDialog (T4.1).
 *
 * Type-name-to-confirm guard (parity with GitHub repo / Stripe key deletion).
 * The [Revoke] button stays disabled until the typed value matches the key
 * name EXACTLY (case-sensitive, NFC-normalised on both sides — BR-R-1). Uses
 * role="alertdialog" with [Cancel] as default focus (Q-FOCUS1, WCAG 3.3.4).
 * Idempotent re-revoke surfaces a distinct info toast (BR-R-5).
 */

import { useRef, useState, useTransition, type RefObject } from 'react';
import { Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import type { KeyEntry } from '@/lib/api/me-keys';
import { revokeMyKey } from '@/app/[locale]/(console)/keys/_actions/revoke-key';

export interface RevokeKeyDialogProps {
  keyEntry: KeyEntry;
  onClose: () => void;
  notify: (variant: 'success' | 'error' | 'info', messageKey: string) => void;
  onMutated: () => void;
  triggerRef?: RefObject<HTMLElement | null>;
}

const nfc = (s: string) => s.normalize('NFC');

export function RevokeKeyDialog({ keyEntry, onClose, notify, onMutated, triggerRef }: RevokeKeyDialogProps) {
  const t = useTranslations('account.keys');
  const tRoot = useTranslations();
  const [typed, setTyped] = useState('');
  const [toast, setToast] = useState<string | null>(null);
  const [isPending, startTransition] = useTransition();
  const cancelRef = useRef<HTMLButtonElement | null>(null);

  const matches = nfc(typed) === nfc(keyEntry.name);
  const revokeDisabled = !matches || isPending;

  function onRevoke() {
    if (revokeDisabled) return;
    setToast(null);
    startTransition(async () => {
      const res = await revokeMyKey({ api_key_id: keyEntry.api_key_id });
      if (res.ok) {
        if (res.response.was_already_revoked) {
          notify('info', 'account.keys.revoke.already_revoked');
        } else {
          notify('success', 'account.keys.revoke.success');
        }
        onMutated();
        onClose();
        return;
      }
      const code = res.error.code;
      if (code === 'account.keys.revoke.not_found') {
        notify('error', code);
        onMutated();
        onClose();
        return;
      }
      setToast(tRoot(code));
    });
  }

  return (
    <Dialog
      titleId="revoke-key-title"
      role="alertdialog"
      closeOnOverlay={false}
      onClose={onClose}
      restoreFocusTo={triggerRef}
      initialFocusRef={cancelRef}
    >
      <h2 id="revoke-key-title" className="text-lg font-semibold">
        {t('revoke.confirm.title')}
      </h2>

      <p role="alert" aria-live="assertive" className="mt-2 text-sm text-neutral-700">
        {t('revoke.warning')}
      </p>

      <p className="mt-3 text-sm">
        <span className="font-medium">{keyEntry.name}</span>{' '}
        <code className="font-mono text-xs text-neutral-500">{keyEntry.key_prefix}…</code>
      </p>

      <div className="mt-4 space-y-1.5">
        <label htmlFor="revoke-confirm-input" className="block text-sm">
          {t('revoke.confirm_label', { name: keyEntry.name })}
        </label>
        <input
          id="revoke-confirm-input"
          type="text"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          aria-label={t('revoke.confirm_aria', { name: keyEntry.name })}
          className="w-full rounded border px-3 py-2"
        />
      </div>

      {toast && (
        <p role="alert" aria-live="assertive" className="mt-3 rounded border border-red-300 bg-red-50 px-3 py-2 text-sm text-red-900">
          {toast}
        </p>
      )}

      <div className="mt-5 flex justify-end gap-2">
        <Button type="button" ref={cancelRef} variant="ghost" onClick={onClose} disabled={isPending}>
          {t('revoke.cancel')}
        </Button>
        <Button type="button" variant="destructive" onClick={onRevoke} disabled={revokeDisabled}>
          {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
          {t('revoke.confirm.cta')}
        </Button>
      </div>
    </Dialog>
  );
}
