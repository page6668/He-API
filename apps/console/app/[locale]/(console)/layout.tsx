// Story 2.5 — (console) route group layout.
//
// Mirrors the (auth) layout's cookie-presence-only gate (TS-CONS-012). The
// real auth enforcement is api-gateway middleware (Story 2.2 BR-3.7); this
// layout just bounces unauthenticated users to /{locale}/signin so they
// don't see a flash-of-empty-state on a protected page.
//
// The Settings sidebar (Profile / Security / Data) is rendered here so all
// authenticated routes share the chrome (front-end-spec §P-11).

import type { ReactNode } from 'react';
import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';
import { getTranslations } from 'next-intl/server';

import { defaultLocale, isLocale } from '@/i18n/config';
import { ACCESS_COOKIE } from '@/lib/auth/cookies';

interface ConsoleLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default async function ConsoleLayout({
  children,
  params: { locale },
}: ConsoleLayoutProps) {
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;

  // Cookie-presence check only (HttpOnly cookies are opaque to JS — the JWT
  // itself is verified by api-gateway). Defence-in-depth pairing with the
  // gateway's JWT middleware (Story 2.2 BR-3.7).
  const hasAccessCookie = cookies().get(ACCESS_COOKIE);
  if (!hasAccessCookie) {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/profile`)}`);
  }

  const t = await getTranslations('account');

  return (
    <div className="mx-auto flex max-w-5xl gap-6 px-6 py-8">
      <aside className="w-48 shrink-0" aria-label={t('settings.sidebar.label')}>
        <nav className="flex flex-col gap-1 text-sm">
          <a
            href={`/${resolvedLocale}/settings/profile`}
            className="rounded px-3 py-2 hover:bg-neutral-100"
          >
            {t('settings.sidebar.profile')}
          </a>
          <a
            href={`/${resolvedLocale}/settings/security`}
            className="rounded px-3 py-2 hover:bg-neutral-100"
          >
            {t('settings.sidebar.security')}
          </a>
          <a
            href={`/${resolvedLocale}/settings/data`}
            className="rounded px-3 py-2 hover:bg-neutral-100"
          >
            {t('settings.sidebar.data')}
          </a>
        </nav>
      </aside>
      <section className="flex-1">{children}</section>
    </div>
  );
}
