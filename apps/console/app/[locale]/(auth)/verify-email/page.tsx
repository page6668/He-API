import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { verifyEmailAction, type VerifyEmailResult } from '@/app/[locale]/_actions/auth';

import { ResendModal } from '../signup/check-inbox/ResendModal';

interface VerifyEmailPageProps {
  params: { locale: string };
  searchParams: { token?: string };
}

/**
 * VerifyEmailPage is a Server Component that calls the verifyEmailAction
 * Server Action synchronously on render. That way users can complete email
 * verification by clicking the email link with JavaScript disabled — the
 * page renders with the verified state on the very first GET.
 *
 * (UNIT-102 + AC2 UI Interaction explicitly require this — the path
 * must work without client-side JS.)
 */
export default async function VerifyEmailPage({
  params: { locale },
  searchParams: { token },
}: VerifyEmailPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const t = await getTranslations();

  // Pass an empty string when ?token=… is missing — the zod schema in the
  // action surfaces this as an invalidToken error, which the page then
  // shows alongside the resend CTA.
  const result: VerifyEmailResult = await verifyEmailAction({ token: token ?? '' });

  if (result.ok && result.status === 'email_verified') {
    return (
      <section aria-labelledby="verify-success" className="space-y-4 text-center">
        <h1 id="verify-success" className="text-2xl font-semibold text-emerald-700">
          {t('verifyEmail.successTitle')}
        </h1>
        <p className="text-sm text-neutral-700">{t('verifyEmail.successDescription')}</p>
        <a
          href={`/${resolvedLocale}/signin`}
          className="inline-block rounded-md bg-blue-600 px-4 py-2 text-white hover:bg-blue-700"
        >
          {t('verifyEmail.successCta')}
        </a>
      </section>
    );
  }

  if (result.ok && result.status === 'already_verified') {
    return (
      <section aria-labelledby="verify-already" className="space-y-4 text-center">
        <h1 id="verify-already" className="text-2xl font-semibold">
          {t('verifyEmail.alreadyVerifiedTitle')}
        </h1>
        <p className="text-sm text-neutral-700">{t('verifyEmail.alreadyVerifiedDescription')}</p>
        <a
          href={`/${resolvedLocale}/signin`}
          className="inline-block rounded-md bg-blue-600 px-4 py-2 text-white hover:bg-blue-700"
        >
          {t('verifyEmail.alreadyVerifiedCta')}
        </a>
      </section>
    );
  }

  // Error path — token invalid / expired / used. The resend modal lets the
  // user request a fresh link (the 410_token_used path can't be recovered
  // by resend either, but the UX is the same — try again).
  const errorTitleKey =
    result.ok === false && result.code === 'auth.errors.tokenAlreadyUsed'
      ? 'verifyEmail.tokenUsedTitle'
      : 'verifyEmail.tokenExpiredTitle';
  const errorBodyKey =
    result.ok === false && result.code === 'auth.errors.tokenAlreadyUsed'
      ? 'verifyEmail.tokenUsedDescription'
      : 'verifyEmail.tokenExpiredDescription';

  return (
    <section aria-labelledby="verify-error" className="space-y-4 text-center">
      <h1 id="verify-error" className="text-2xl font-semibold text-amber-700">
        {t(errorTitleKey)}
      </h1>
      <p className="text-sm text-neutral-700">{t(errorBodyKey)}</p>
      <ResendModal />
    </section>
  );
}
