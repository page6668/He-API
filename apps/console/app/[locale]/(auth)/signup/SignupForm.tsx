'use client';

import { useFormState, useFormStatus } from 'react-dom';
import { useTranslations } from 'next-intl';

import type { Locale } from '@/i18n/config';
import {
  registerUserForm,
  type RegisterUserResult,
} from '@/app/[locale]/_actions/auth';
import { Button, Notice, fieldCls, labelCls } from '@/components/ui/kit';

interface SignupFormProps {
  locale: Locale;
}

function fieldErrorKey(result: RegisterUserResult | null, field: 'email' | 'password'): string | undefined {
  if (!result || result.ok) return undefined;
  return result.fieldErrors?.[field];
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

export function SignupForm({ locale }: SignupFormProps) {
  // 命名空间 = 文件名:messages/<locale>/auth.json → `auth`。scope 在 'auth' 上,
  // t('signup.xxx') 解析 auth.signup.xxx、t('errors.xxx') 解析 auth.errors.xxx。
  // 无参 useTranslations() 会让每个 key 都 MISSING_MESSAGE 并让本页 500。
  const t = useTranslations('auth');
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

      {/* 行内细条,不是满宽红底 banner。 */}
      {primaryErrorKey ? (
        <Notice tone="error" role="alert">
          {/* The i18n key is fully qualified ("auth.errors.X"); we strip the
              "auth." prefix because this form is scoped to the `auth`
              namespace, so "errors.X" resolves auth.errors.X. */}
          {t(primaryErrorKey.replace(/^auth\./, ''))}
          {'retryAfterSeconds' in state! && state.retryAfterSeconds ? (
            <span className="tabular"> ({state.retryAfterSeconds}s)</span>
          ) : null}
        </Notice>
      ) : null}

      <div>
        <label htmlFor="email" className={labelCls}>
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
          className={fieldCls}
        />
        {emailErrorKey ? (
          <p id="email-error" className="mt-1.5 text-small text-crimson">
            {t(emailErrorKey.replace(/^auth\./, ''))}
          </p>
        ) : null}
      </div>

      <div>
        <label htmlFor="password" className={labelCls}>
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
          className={fieldCls}
        />
        <p id="password-hint" className="mt-1.5 text-label text-ink-muted">
          {t('signup.passwordHint')}
        </p>
        {passwordErrorKey ? (
          <p id="password-error" className="mt-1.5 text-small text-crimson">
            {t(passwordErrorKey.replace(/^auth\./, ''))}
          </p>
        ) : null}
      </div>

      <SubmitButton label={t('signup.submit')} submittingLabel={t('signup.submitting')} />

      <p className="text-small text-ink-secondary">
        {t('signup.haveAccount')}{' '}
        <a
          className="text-ink underline decoration-line-strong underline-offset-2 transition-colors duration-state ease-he hover:decoration-ink"
          href={`/${locale}/signin`}
        >
          {t('signup.signinLink')}
        </a>
      </p>
    </form>
  );
}
