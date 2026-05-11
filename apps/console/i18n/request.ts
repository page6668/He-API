import { getRequestConfig } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { locales, defaultLocale, isLocale } from './config';

export default getRequestConfig(async ({ requestLocale }) => {
  const requested = await requestLocale;
  if (!isLocale(requested)) {
    notFound();
  }
  const locale = requested;

  const common = (await import(`../messages/${locale}/common.json`)).default;

  return {
    locale,
    messages: { common },
  };
});

export { locales, defaultLocale };
