import type { ReactNode } from 'react';
import { NextIntlClientProvider } from 'next-intl';
import { getMessages, unstable_setRequestLocale } from 'next-intl/server';
import { notFound } from 'next/navigation';
import { IBM_Plex_Sans, IBM_Plex_Mono } from 'next/font/google';
import { locales, isLocale } from '@/i18n/config';
import { isRtlLocale } from '@/lib/i18n';
import { LocaleSwitch } from '@/components/LocaleSwitch';
import { Logo } from '@/components/brand/Logo';
import '../globals.css';

/**
 * 字体:IBM Plex Sans / Mono —— next/font/google 在**构建时**下载并**自托管**到本域,
 * 运行时不请求 Google CDN(大陆不可达)。仅 latin subset;中文落系统栈(PingFang/雅黑),
 * 刻意不加载 CJK webfont(全量 10MB+ 会毁首屏)。见 knowledge/taste/design-system.md。
 */
const plexSans = IBM_Plex_Sans({
  subsets: ['latin'],
  weight: ['400', '500', '600'],
  variable: '--font-plex-sans',
  display: 'swap',
});
const plexMono = IBM_Plex_Mono({
  subsets: ['latin'],
  weight: ['400', '500', '600'],
  variable: '--font-plex-mono',
  display: 'swap',
});

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

  // 导航只列**真实存在**的路由。曾有一条 `/docs` 死链(该路由从未实现 → 404),
  // 以及把「Console」指向 `[locale]/` 这个 demo 占位页(看着像空白);均已修正。
  // 新增路由时务必同步这里,并确认目标页面存在。
  const nav = [
    { href: `/${locale}/dashboard`, label: 'Console' },
    { href: `/${locale}/models`, label: 'Models' },
    { href: `/${locale}/playground`, label: 'Playground' },
    { href: `/${locale}/benchmark`, label: 'Benchmark' },
    { href: `/${locale}/docs`, label: 'Docs' },
  ];

  return (
    <html lang={locale} dir={dir} className={`${plexSans.variable} ${plexMono.variable}`}>
      <body className="bg-paper font-sans text-body text-ink antialiased">
        <NextIntlClientProvider locale={locale} messages={messages}>
          <a href="#main" className="sr-only focus:not-sr-only">
            {/* a11y skip-to-content link; full text via t('a11y.skipToContent') in inner client wrapper */}
          </a>
          {/* 顶栏:56px 高、1px 暖边框分隔、内容限宽居中(禁止内容裸贴视口边缘) */}
          <header className="border-b border-line bg-surface">
            <div className="mx-auto flex h-14 max-w-playground items-center justify-between gap-6 px-6 lg:px-8">
              <div className="flex items-center gap-7">
                <a href={`/${locale}/`} className="shrink-0" aria-label="He-API">
                  <Logo size={22} />
                </a>
                <nav className="hidden items-center gap-5 text-small text-ink-secondary sm:flex">
                  {nav.map((item) => (
                    <a
                      key={item.href}
                      href={item.href}
                      className="transition-colors duration-state ease-he hover:text-ink"
                    >
                      {item.label}
                    </a>
                  ))}
                </nav>
              </div>
              <LocaleSwitch currentLocale={locale} />
            </div>
          </header>
          <main id="main">{children}</main>
        </NextIntlClientProvider>
      </body>
    </html>
  );
}
