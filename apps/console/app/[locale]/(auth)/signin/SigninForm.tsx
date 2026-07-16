'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import type { Locale } from '@/i18n/config';
import {
  signinActionForm,
  type SigninResult,
} from '@/app/[locale]/_actions/auth';
import { Button, Notice, fieldCls, labelCls } from '@/components/ui/kit';

import { ResendModal } from '../signup/check-inbox/ResendModal';

interface SigninFormProps {
  locale: Locale;
  prefillEmail?: string;
}

/** 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2)。 */
function SubmitButton({ label, submittingLabel }: { label: string; submittingLabel: string }) {
  const { pending } = useFormStatus();
  return (
    <Button type="submit" variant="primary" disabled={pending} className="w-full">
      {pending ? submittingLabel : label}
    </Button>
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

      {/* 行内细条,不是满宽红底 banner(design-system.md dashboard/空错状态同调)。 */}
      {errorCode ? (
        <Notice tone="error" role="alert">
          <p>
            {t(errorCode.replace(/^auth\./, ''))}
            {retryAfterSeconds ? (
              <span className="tabular"> ({retryAfterSeconds}s)</span>
            ) : null}
          </p>
          {emailNotVerified ? (
            <div className="mt-2">
              <ResendModal />
            </div>
          ) : null}
        </Notice>
      ) : null}

      <div>
        <label htmlFor="email" className={labelCls}>
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
          className={fieldCls}
        />
      </div>

      <div>
        <div className="flex items-center justify-between gap-3">
          <label htmlFor="password" className={labelCls}>
            {t('signin.passwordLabel')}
          </label>
          <a
            className="text-label text-ink-secondary underline decoration-line-strong underline-offset-2 transition-colors duration-state ease-he hover:text-ink"
            href={`/${locale}/forgot-password`}
          >
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
          className={fieldCls}
        />
      </div>

      <SubmitButton label={t('signin.submit')} submittingLabel={t('signin.submitting')} />

      <p className="text-small text-ink-secondary">
        {t('signin.noAccount')}{' '}
        <a
          className="text-ink underline decoration-line-strong underline-offset-2 transition-colors duration-state ease-he hover:decoration-ink"
          href={`/${locale}/signup`}
        >
          {t('signin.signupLink')}
        </a>
      </p>
    </form>
  );
}
