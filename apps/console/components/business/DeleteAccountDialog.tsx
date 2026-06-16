'use client';

// Story 2.7 AC1 — Settings → Data "Danger Zone": the "Delete My Account" CTA +
// the re-authentication alertdialog. Re-auth fields branch on the
// server-prefetched account shape (BR-1.3/1.4): password-users get a password
// field; OAuth-only users a type-exact-email confirm; any totp_enabled user an
// additional 6-digit TOTP. Confirm stays disabled until every required field is
// satisfied (WCAG 3.3.4 — default focus on Cancel). Mirrors RevokeKeyDialog.

import { useRef, useState, useTransition } from 'react';
import { Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { useRouter } from 'next/navigation';

import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { LtrText } from '@/components/business/LtrText';
import { requestAccountDeletion, type DeletionState } from '@/lib/account/deletion-actions';

export interface DeleteAccountDialogProps {
  /** Server-prefetched account shape (GET /v1/account/deletion). */
  state: DeletionState;
  /** The account email — drives the OAuth-only exact-match confirm (BR-1.4). */
  email: string;
  /** Current locale — for the post-success redirect to the recovery page. */
  locale: string;
}

const nfc = (s: string) => s.normalize('NFC');
const inputClass = 'mt-1 w-full rounded border border-neutral-300 px-3 py-2 text-sm';

export function DeleteAccountDialog({ state, email, locale }: DeleteAccountDialogProps) {
  const t = useTranslations('account.delete');
  const router = useRouter();

  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState('');
  const [confirmEmail, setConfirmEmail] = useState('');
  const [totp, setTotp] = useState('');
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);
  const [isPending, startTransition] = useTransition();

  const ctaRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);

  const isPendingDeletion = state.status === 'pending_deletion';

  // Required-field gating (BR-1.4 / UNIT-004/005/006).
  const passwordOk = state.has_password ? password.length > 0 : true;
  const emailOk = state.has_password ? true : confirmEmail.length > 0 && nfc(confirmEmail) === nfc(email);
  const totpOk = state.totp_enabled ? /^[0-9]{6}$/.test(totp) : true;
  const confirmDisabled = !(passwordOk && emailOk && totpOk) || isPending;

  function close() {
    setOpen(false);
    setPassword('');
    setConfirmEmail('');
    setTotp('');
    setFieldError(null);
    setToast(null);
  }

  function onConfirm() {
    setFieldError(null);
    setToast(null);
    const wasTotp = state.totp_enabled;
    startTransition(async () => {
      const res = await requestAccountDeletion({
        password: state.has_password ? password : undefined,
        confirm_email: state.has_password ? undefined : confirmEmail,
        totp_code: state.totp_enabled ? totp : undefined,
      });
      switch (res.kind) {
        case 'ok':
        case 'not_deletable':
          // 200 (new or idempotent) → recovery. 409 routes there too for a
          // graceful idempotent UX (INT-004 spirit).
          router.push(`/${locale}/account/recovery`);
          return;
        case 'bad_reauth':
          // Clear sensitive fields, keep the dialog open (INT-002).
          setPassword('');
          setTotp('');
          setFieldError(wasTotp && totp ? t('dialog.errors.bad_totp') : t('dialog.errors.bad_password'));
          return;
        case 'rate_limited':
          setToast(t('errors.rate_limited'));
          return;
        case 'unauthorized':
          router.push(`/${locale}/signin`);
          return;
        default:
          setToast(t('errors.generic'));
      }
    });
  }

  return (
    <section aria-labelledby="danger-zone-heading" className="mt-10 rounded-lg border border-red-200 p-6">
      <h2 id="danger-zone-heading" className="text-lg font-semibold text-red-700">
        {t('cta.label')}
      </h2>
      <p className="mt-1 text-sm text-neutral-600">{t('dialog.consequences')}</p>
      <div className="mt-4">
        <Button
          ref={ctaRef}
          variant="destructive"
          disabled={isPendingDeletion}
          title={isPendingDeletion ? t('cta.disabled_pending') : undefined}
          onClick={() => setOpen(true)}
        >
          {t('cta.label')}
        </Button>
      </div>

      {open && (
        <Dialog
          titleId="delete-account-title"
          role="alertdialog"
          closeOnOverlay={false}
          onClose={close}
          restoreFocusTo={ctaRef}
          initialFocusRef={cancelRef}
        >
          <h3 id="delete-account-title" className="text-lg font-semibold">
            {t('dialog.title')}
          </h3>
          <p role="alert" aria-live="assertive" className="mt-2 text-sm text-neutral-700">
            {t('dialog.consequences')}
          </p>

          <div className="mt-4 space-y-3">
            {state.has_password ? (
              <label className="block text-sm font-medium">
                {t('dialog.password_label')}
                <input
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className={inputClass}
                />
              </label>
            ) : (
              <label className="block text-sm font-medium">
                {t('dialog.email_confirm_label')}
                <LtrText as="div">
                  <input
                    type="email"
                    autoComplete="off"
                    value={confirmEmail}
                    onChange={(e) => setConfirmEmail(e.target.value)}
                    className={inputClass}
                  />
                </LtrText>
              </label>
            )}
            {state.totp_enabled && (
              <label className="block text-sm font-medium">
                {t('dialog.totp_label')}
                <LtrText as="div">
                  <input
                    inputMode="numeric"
                    pattern="[0-9]*"
                    maxLength={6}
                    autoComplete="one-time-code"
                    value={totp}
                    onChange={(e) => setTotp(e.target.value.replace(/\D/g, '').slice(0, 6))}
                    className={inputClass}
                  />
                </LtrText>
              </label>
            )}
            {fieldError && (
              <p role="alert" className="text-sm text-red-600">
                {fieldError}
              </p>
            )}
            {toast && (
              <p role="status" className="text-sm text-red-600">
                {toast}
              </p>
            )}
          </div>

          <div className="mt-6 flex justify-end gap-2">
            <Button ref={cancelRef} variant="ghost" onClick={close}>
              {t('dialog.cancel')}
            </Button>
            <Button variant="destructive" disabled={confirmDisabled} onClick={onConfirm}>
              {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
              {t('dialog.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
