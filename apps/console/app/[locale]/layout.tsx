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
    <html lang={locale} dir={dir} className="scroll-smooth">
      <body className="min-h-screen antialiased">
        <NextIntlClientProvider locale={locale} messages={messages}>
          <a href="#main" className="sr-only focus:not-sr-only">
          </a>
          <header className="sticky top-0 z-50 border-b bg-white/80 dark:bg-slate-950/80 backdrop-blur-md">
            <div className="mx-auto flex max-w-7xl items-center justify-between px-6 py-3">
              <nav className="flex items-center gap-6">
                <div className="flex items-center gap-2">
                  <div className="relative flex h-9 w-9 items-center justify-center rounded-lg bg-gradient-to-br from-orange-500 to-blue-500 shadow-md">
                    <span className="text-sm font-bold text-white">H</span>
                    <div className="absolute -right-1 -top-1 h-2.5 w-2.5 rounded-full bg-green-400 ring-2 ring-white dark:ring-slate-900 animate-pulse"></div>
                  </div>
                  <div className="flex flex-col">
                    <span className="text-sm font-semibold leading-tight text-slate-900 dark:text-white">
                      He-API
                    </span>
                    <span className="text-[10px] leading-tight text-slate-500 dark:text-slate-400">
                      AI Gateway
                    </span>
                  </div>
                </div>
                <div className="hidden items-center gap-1 text-sm sm:flex">
                  <a 
                    href={`/${locale}/`} 
                    className="rounded-md px-3 py-1.5 text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                  >
                    Console
                  </a>
                  <a 
                    href={`/${locale}/playground`} 
                    className="rounded-md px-3 py-1.5 text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                  >
                    Playground
                  </a>
                  <a 
                    href={`/${locale}/benchmark`} 
                    className="rounded-md px-3 py-1.5 text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                  >
                    Benchmark
                  </a>
                  <a 
                    href={`/${locale}/docs`} 
                    className="rounded-md px-3 py-1.5 text-slate-600 transition-colors hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
                  >
                    Docs
                  </a>
                </div>
              </nav>
              <div className="flex items-center gap-3">
                <LocaleSwitch currentLocale={locale} />
              </div>
            </div>
          </header>
          <main id="main" className="animate-page-enter">{children}</main>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
