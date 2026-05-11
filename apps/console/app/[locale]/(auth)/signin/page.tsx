import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { SigninForm } from './SigninForm';

interface SigninPageProps {
  params: { locale: string };
  searchParams: { email?: string };
}

/**
 * SigninPage renders the email + password form. The `?email=` query param
 * (set by the verify-email "Sign in" CTA per AC2 main scenario) pre-fills
 * the email field so a freshly verified user doesn't have to re-type
 * their address — UNIT-166.
 */
export default async function SigninPage({
  params: { locale },
  searchParams: { email },
}: SigninPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const t = await getTranslations('signin');
  const prefillEmail = typeof email === 'string' ? email : '';

  return (
    <section aria-labelledby="signin-title" className="space-y-6">
      <header className="space-y-1">
        <h1 id="signin-title" className="text-2xl font-semibold">
          {t('title')}
        </h1>
        <p className="text-sm text-neutral-600">{t('subtitle')}</p>
      </header>
      <SigninForm locale={resolvedLocale} prefillEmail={prefillEmail} />
    </section>
  );
}
