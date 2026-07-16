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

  // 命名空间 = 文件名:messages/<locale>/auth.json → `auth`;文案在 auth.signup.*。
  // 写成 getTranslations('signup') 会 MISSING_MESSAGE 并让本页 500。
  const t = await getTranslations('auth.signup');

  return (
    <section aria-labelledby="signup-title" className="space-y-6">
      <header className="space-y-1">
        <h1 id="signup-title" className="text-h2 text-ink">
          {t('title')}
        </h1>
        <p className="text-small text-ink-secondary">{t('subtitle')}</p>
      </header>
      {/* Story 2.3 — OAuth signup entry; renders above the password form. */}
      <OAuthButtonGroup locale={resolvedLocale} returnTo={`/${resolvedLocale}/onboarding/welcome`} />
      <SignupForm locale={resolvedLocale} />
    </section>
  );
}
