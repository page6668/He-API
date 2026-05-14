import { unstable_setRequestLocale } from 'next-intl/server';
import { getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale } from '@/i18n/config';
import { OAuthButtonGroup } from '@/components/business/OAuthButtonGroup';
import { SignupForm } from './SignupForm';

interface SignupPageProps {
  params: { locale: string };
}

export default async function SignupPage({ params: { locale } }: SignupPageProps) {
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  const t = await getTranslations('signup');

  return (
    <section aria-labelledby="signup-title" className="space-y-6">
      <header className="space-y-1">
        <h1 id="signup-title" className="text-2xl font-semibold">
          {t('title')}
        </h1>
        <p className="text-sm text-neutral-600">{t('subtitle')}</p>
      </header>
      {/* Story 2.3 — OAuth signup entry; renders above the password form. */}
      <OAuthButtonGroup locale={resolvedLocale} returnTo={`/${resolvedLocale}/onboarding/welcome`} />
      <SignupForm locale={resolvedLocale} />
    </section>
  );
}
