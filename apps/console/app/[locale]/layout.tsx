import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages, unstable_setRequestLocale } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { locales, isLocale } from '@/i18n/config';
import { isRtlLocale } from '@/lib/i18n';
import { LocaleSwitch } from '@/components/LocaleSwitch';
import '../globals.css';

export function generateStaticParams() {
  return locales.map((locale) => ({ locale }));
}

interface LocaleLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default async function LocaleLayout({ children, params: { locale } }: LocaleLayoutProps) {
  if (!isLocale(locale)) {
    notFound();
  }
  unstable_setRequestLocale(locale);

  const messages = await getMessages();
  const dir = isRtlLocale(locale) ? 'rtl' : 'ltr';

  return (
    <html lang={locale} dir={dir}>
      <body>
        <NextIntlClientProvider locale={locale} messages={messages}>
          <a href="#main" className="sr-only focus:not-sr-only">
            {/* a11y skip-to-content link; full text via t('a11y.skipToContent') in inner client wrapper */}
          </a>
          <header className="flex items-center justify-between border-b px-6 py-3">
            <nav className="flex items-center gap-4 text-sm">
              <a href={`/${locale}/`}>Console</a>
              <a href={`/${locale}/docs`}>Docs</a>
            </nav>
            <LocaleSwitch currentLocale={locale} />
          </header>
          <main id="main">{children}</main>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
