'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import type { Locale } from '@/i18n/config';
import {
  registerUserForm,
  type RegisterUserResult,
} from '@/app/[locale]/_actions/auth';

interface SignupFormProps {
  locale: Locale;
}

function fieldErrorKey(result: RegisterUserResult | null, field: 'email' | 'password'): string | undefined {
  if (!result || result.ok) return undefined;
  return result.fieldErrors?.[field];
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

export function SignupForm({ locale }: SignupFormProps) {
  const t = useTranslations();
  const [state, formAction] = useFormState<RegisterUserResult | null, FormData>(registerUserForm, null);

  // The top-level alert announces the primary error code (the form-level
  // message) for screen readers via aria-live.
  const hasError = state && !state.ok;
  const primaryErrorKey = hasError ? state.code : undefined;
  const emailErrorKey = fieldErrorKey(state, 'email');
  const passwordErrorKey = fieldErrorKey(state, 'password');

  return (
    <form action={formAction} className="space-y-4" noValidate>
      {/* The Server Action binds the locale via this hidden field so the
          form-action wrapper (which is a `(prevState, formData) => state`
          shape) can extract it without depending on a closure variable. */}
      <input type="hidden" name="locale" value={locale} />

      {primaryErrorKey ? (
        <div
          role="alert"
          aria-live="polite"
          className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800"
        >
          {/* The i18n key is fully qualified ("auth.errors.X"); we strip the
              "auth." prefix because this form is rendered with
              `useTranslations()` rooted at "auth.signup". The errors live in
              a sibling namespace so we resolve them via a root-relative key. */}
          {t(primaryErrorKey.replace(/^auth\./, ''))}
          {'retryAfterSeconds' in state! && state.retryAfterSeconds ? (
            <span> ({state.retryAfterSeconds}s)</span>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-1">
        <label htmlFor="email" className="block text-sm font-medium">
          {t('signup.emailLabel')}
        </label>
        <input
          id="email"
          name="email"
          type="email"
          required
          autoComplete="email"
          aria-invalid={Boolean(emailErrorKey)}
          aria-describedby={emailErrorKey ? 'email-error' : undefined}
          placeholder={t('signup.emailPlaceholder')}
          className="block w-full rounded-md border border-neutral-300 px-3 py-2 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200"
        />
        {emailErrorKey ? (
          <p id="email-error" className="text-sm text-red-700">
            {t(emailErrorKey.replace(/^auth\./, ''))}
          </p>
        ) : null}
      </div>

      <div className="space-y-1">
        <label htmlFor="password" className="block text-sm font-medium">
          {t('signup.passwordLabel')}
        </label>
        <input
          id="password"
          name="password"
          type="password"
          required
          minLength={10}
          autoComplete="new-password"
          aria-invalid={Boolean(passwordErrorKey)}
          aria-describedby={passwordErrorKey ? 'password-error' : 'password-hint'}
          className="block w-full rounded-md border border-neutral-300 px-3 py-2 focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-200"
        />
        <p id="password-hint" className="text-xs text-neutral-600">
          {t('signup.passwordHint')}
        </p>
        {passwordErrorKey ? (
          <p id="password-error" className="text-sm text-red-700">
            {t(passwordErrorKey.replace(/^auth\./, ''))}
          </p>
        ) : null}
      </div>

      <SubmitButton label={t('signup.submit')} submittingLabel={t('signup.submitting')} />

      <p className="text-center text-sm text-neutral-600">
        {t('signup.haveAccount')}{' '}
        <a className="text-blue-700 underline" href={`/${locale}/signin`}>
          {t('signup.signinLink')}
        </a>
      </p>
    </form>
  );
}
