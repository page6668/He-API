import { getRequestConfig } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { locales, defaultLocale, isLocale } from './config';
import { loadMessages } from './load-messages';

export default getRequestConfig(async ({ requestLocale }) => {
  const requested = await requestLocale;
  if (!isLocale(requested)) {
    notFound();
  }
  const locale = requested;

  // Story 10.1 (BR-10.1.1, linchpin) — load EVERY namespace for the requested
  // locale, not just `common`. Previously only `common` was provided, so every
  // page using getTranslations('account'|'dashboard'|'logs'|'models') /
  // useTranslations('auth') hit next-intl MISSING_MESSAGE under the real request
  // config (the gap that component-level test providers masked). The loader is
  // extracted to ./load-messages so it is unit-testable outside a request scope.
  return {
    locale,
    messages: await loadMessages(locale),
  };
});

export { locales, defaultLocale };
