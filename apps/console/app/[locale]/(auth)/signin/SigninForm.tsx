'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import type { Locale } from '@/i18n/config';
import {
  signinActionForm,
  type SigninResult,
} from '@/app/[locale]/_actions/auth';

import { ResendModal } from '../signup/check-inbox/ResendModal';

interface SigninFormProps {
  locale: Locale;
  prefillEmail?: string;
}

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

export function SigninForm({ locale, prefillEmail = '' }: SigninFormProps) {
  // messages live under the `auth` namespace (messages/<locale>/auth.json →
  // auth.signin.*); scope here so t('signin.xxx') resolves auth.signin.xxx.
  const t = useTranslations('auth');
  const [state, formAction] = useFormState<SigninResult | null, FormData>(
    signinActionForm,
    null,
  );

  // Form-level error message + accompanying CTA branches:
  //   - emailNotVerified → render ResendModal CTA so user can request a new link
  //   - accountLocked    → render countdown using retryAfterSeconds
  //   - everything else  → plain message
  const hasError = state && !state.ok;
  const errorCode = hasError ? state.code : undefined;
  const emailNotVerified = hasError && state.emailNotVerified;
  const retryAfterSeconds = hasError ? state.retryAfterSeconds : undefined;

  return (
    <form action={formAction} className="space-y-4" noValidate>
      <input type="hidden" name="locale" value={locale} />

      {errorCode ? (
        <div
          role="alert"
          aria-live="polite"
          className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800"
        >
          <p>
            {t(errorCode.replace(/^auth\./, ''))}
            {retryAfterSeconds ? <span> ({retryAfterSeconds}s)</span> : null}
          </p>
          {emailNotVerified ? (
            <div className="mt-2">
              <ResendModal />
            </div>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-1">
        <label htmlFor="email" className="block text-sm font-medium">
          {t('signin.emailLabel')}
        </label>
        <input
          id="email"
          name="email"
          type="email"
          required
          autoComplete="email"
          autoFocus={!prefillEmail}
          defaultValue={prefillEmail}
          placeholder={t('signin.emailPlaceholder')}
          className="block w-full rounded-md border border-neutral-300 px-3 py-2 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200"
        />
      </div>

      <div className="space-y-1">
        <div className="flex items-center justify-between">
          <label htmlFor="password" className="block text-sm font-medium">
            {t('signin.passwordLabel')}
          </label>
          <a className="text-xs text-blue-700 hover:underline" href={`/${locale}/forgot-password`}>
            {t('signin.forgotPassword')}
          </a>
        </div>
        <input
          id="password"
          name="password"
          type="password"
          required
          autoComplete="current-password"
          autoFocus={Boolean(prefillEmail)}
          className="block w-full rounded-md border border-neutral-300 px-3 py-2 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200"
        />
      </div>

      <SubmitButton label={t('signin.submit')} submittingLabel={t('signin.submitting')} />

      <p className="text-center text-sm text-neutral-600">
        {t('signin.noAccount')}{' '}
        <a className="text-blue-700 underline" href={`/${locale}/signup`}>
          {t('signin.signupLink')}
        </a>
      </p>
    </form>
  );
}
