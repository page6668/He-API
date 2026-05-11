'use client';

import { useState } from 'react';
import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import {
  resendVerificationForm,
  type ResendVerificationResult,
} from '@/app/[locale]/_actions/auth';

/**
 * ResendModal is the client-side dialog the user opens from either:
 *   - the check-inbox page (right after signup, when the email hasn't arrived)
 *   - the verify-email error page (when the link expired or was already used)
 *
 * It binds `resendVerificationForm` (the (prev, formData) → state wrapper)
 * via React 19's useFormState. The per-form-element zod validation lives
 * inside the Server Action; the modal just renders the i18n key the
 * action returns. m-5 anti-enumeration: the success message is the same
 * regardless of whether the email was sent / unknown / already-verified.
 */
function SubmitButton({ label, submittingLabel }: { label: string; submittingLabel: string }) {
  const { pending } = useFormStatus();
  return (
    <button
      type="submit"
      disabled={pending}
      className="w-full rounded-md bg-blue-600 px-4 py-2 text-white hover:bg-blue-700 disabled:cursor-not-allowed disabled:opacity-60"
    >
      {pending ? submittingLabel : label}
    </button>
  );
}

export function ResendModal() {
  const t = useTranslations();
  const [open, setOpen] = useState(false);
  const [state, formAction] = useFormState<ResendVerificationResult | null, FormData>(
    resendVerificationForm,
    null,
  );

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="text-sm text-blue-700 underline hover:text-blue-900"
      >
        {t('signupCheckInbox.resendCta')}
      </button>
    );
  }

  // Success branch — the action returned ok=true. Per m-5 we render a
  // generic "we sent you a new link" message — no signal about whether
  // the email is actually registered.
  if (state && state.ok) {
    return (
      <div
        role="status"
        aria-live="polite"
        className="rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800"
      >
        <p className="font-medium">{t('verifyEmail.resendSentTitle')}</p>
        <p>{t('verifyEmail.resendSentDescription')}</p>
      </div>
    );
  }

  const errorKey = state && !state.ok ? state.code : undefined;
  const retryAfterSeconds = state && !state.ok ? state.retryAfterSeconds : undefined;

  return (
    <div
      role="dialog"
      aria-labelledby="resend-modal-title"
      className="rounded-md border border-neutral-200 bg-white p-4 text-left shadow-sm"
    >
      <h2 id="resend-modal-title" className="text-lg font-semibold">
        {t('verifyEmail.resendModalTitle')}
      </h2>
      <p className="mt-1 text-sm text-neutral-600">{t('verifyEmail.resendModalDescription')}</p>
      <form action={formAction} className="mt-3 space-y-3" noValidate>
        {errorKey ? (
          <div
            role="alert"
            aria-live="polite"
            className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800"
          >
            {t(errorKey.replace(/^auth\./, ''))}
            {retryAfterSeconds ? <span> ({retryAfterSeconds}s)</span> : null}
          </div>
        ) : null}
        <div className="space-y-1">
          <label htmlFor="resend-email" className="block text-sm font-medium">
            {t('signup.emailLabel')}
          </label>
          <input
            id="resend-email"
            name="email"
            type="email"
            required
            autoComplete="email"
            placeholder={t('signup.emailPlaceholder')}
            className="block w-full rounded-md border border-neutral-300 px-3 py-2 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200"
          />
        </div>
        <SubmitButton
          label={t('verifyEmail.resendSubmit')}
          submittingLabel={t('verifyEmail.resendSubmitting')}
        />
        <button
          type="button"
          onClick={() => setOpen(false)}
          className="block w-full text-center text-sm text-neutral-600 hover:text-neutral-900"
        >
          ✕
        </button>
      </form>
    </div>
  );
}
