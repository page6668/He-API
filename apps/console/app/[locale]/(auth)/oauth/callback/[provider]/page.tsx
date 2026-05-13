// callback/[provider]/page.tsx — Story 2.3 P8.
//
// Loading placeholder shown while the api-gateway processes the OAuth
// callback (state validation + provider token exchange + linking decision
// + JWT issuance). The api-gateway 302s away as soon as the upstream call
// completes, so users typically see this page for <1s. The role="status"
// + aria-live="polite" makes it screen-reader friendly per BR-1.10 a11y.
//
// Errors do NOT land here — the api-gateway 302s to /{locale}/signin
// with an ?oauth_error= query param so the toast renders on the signin
// page, NOT this loading shell.

import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';

interface CallbackPageProps {
  params: { locale: string; provider: string };
}

export default async function OAuthCallbackPage({ params: { locale, provider } }: CallbackPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);
  const t = await getTranslations({ locale: resolvedLocale, namespace: 'auth' });

  return (
    <section
      role="status"
      aria-live="polite"
      data-provider={provider}
      className="flex min-h-[40vh] flex-col items-center justify-center space-y-4"
    >
      <div
        className="h-10 w-10 animate-spin rounded-full border-2 border-neutral-300 border-t-neutral-700"
        aria-hidden="true"
      />
      <p className="text-sm text-neutral-600">{t('oauth.callbackLoading')}</p>
    </section>
  );
}
