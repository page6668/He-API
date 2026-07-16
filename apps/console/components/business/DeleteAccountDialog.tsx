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

import { Dialog } from '@/components/ui/dialog';
import { Panel, fieldCls, labelCls } from '@/components/ui/kit';
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

// kit.tsx's Button doesn't forwardRef (React 18) and this file needs refs for
// focus management (restoreFocusTo / initialFocusRef) — hand-rolled native
// <button>s carrying the same design tokens, mirroring Playground.tsx's idiom.
// Destructive action → crimson outline, never seal (design-system.md 铁律2).
const dangerBtnCls =
  'inline-flex items-center rounded-md border border-crimson/40 bg-transparent px-4 py-2 text-small font-medium text-crimson transition-colors duration-state ease-he hover:bg-crimson/5 focus:outline-none focus:ring-2 focus:ring-crimson/25 disabled:cursor-not-allowed disabled:opacity-50';
const ghostBtnCls =
  'rounded-md px-4 py-2 text-small font-medium text-ink-secondary transition-colors duration-state ease-he hover:bg-surface-sunken hover:text-ink focus:outline-none focus:ring-2 focus:ring-ink/15 disabled:cursor-not-allowed disabled:opacity-50';

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
    <section aria-labelledby="danger-zone-heading">
      {/* Danger Zone = a normal Panel with a quiet crimson-toned heading —
          no full-width red banner (design-system.md DANGER ZONE rule). */}
      <Panel className="space-y-4">
        <div>
          <h2 id="danger-zone-heading" className="text-h3 text-crimson">
            {t('cta.label')}
          </h2>
          <p className="mt-1 text-small text-ink-secondary">{t('dialog.consequences')}</p>
        </div>
        <button
          type="button"
          ref={ctaRef}
          disabled={isPendingDeletion}
          title={isPendingDeletion ? t('cta.disabled_pending') : undefined}
          onClick={() => setOpen(true)}
          className={dangerBtnCls}
        >
          {t('cta.label')}
        </button>
      </Panel>

      {open && (
        <Dialog
          titleId="delete-account-title"
          role="alertdialog"
          closeOnOverlay={false}
          onClose={close}
          restoreFocusTo={ctaRef}
          initialFocusRef={cancelRef}
          className="rounded-lg border border-line bg-surface p-6 shadow-overlay"
        >
          <h3 id="delete-account-title" className="text-h3 text-ink">
            {t('dialog.title')}
          </h3>
          <p role="alert" aria-live="assertive" className="mt-2 text-small text-ink-secondary">
            {t('dialog.consequences')}
          </p>

          <div className="mt-4 space-y-3">
            {state.has_password ? (
              <label className="block">
                <span className={labelCls}>{t('dialog.password_label')}</span>
                <input
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className={fieldCls}
                />
              </label>
            ) : (
              <label className="block">
                <span className={labelCls}>{t('dialog.email_confirm_label')}</span>
                <LtrText as="div">
                  <input
                    type="email"
                    autoComplete="off"
                    value={confirmEmail}
                    onChange={(e) => setConfirmEmail(e.target.value)}
                    className={fieldCls}
                  />
                </LtrText>
              </label>
            )}
            {state.totp_enabled && (
              <label className="block">
                <span className={labelCls}>{t('dialog.totp_label')}</span>
                <LtrText as="div">
                  <input
                    inputMode="numeric"
                    pattern="[0-9]*"
                    maxLength={6}
                    autoComplete="one-time-code"
                    value={totp}
                    onChange={(e) => setTotp(e.target.value.replace(/\D/g, '').slice(0, 6))}
                    className={`${fieldCls} tabular`}
                  />
                </LtrText>
              </label>
            )}
            {fieldError && (
              <p role="alert" className="text-small text-crimson">
                {fieldError}
              </p>
            )}
            {toast && (
              <p role="status" className="text-small text-crimson">
                {toast}
              </p>
            )}
          </div>

          <div className="mt-6 flex justify-end gap-2">
            <button type="button" ref={cancelRef} onClick={close} className={ghostBtnCls}>
              {t('dialog.cancel')}
            </button>
            <button
              type="button"
              disabled={confirmDisabled}
              onClick={onConfirm}
              className={dangerBtnCls}
            >
              {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
              {t('dialog.confirm')}
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
