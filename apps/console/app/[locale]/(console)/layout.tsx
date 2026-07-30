import type { ReactNode } from 'react';
import { cookies, headers } from 'next/headers';
import { redirect } from 'next/navigation';
import { getTranslations } from 'next-intl/server';

import { defaultLocale, isLocale } from '@/i18n/config';
import { ACCESS_COOKIE } from '@/lib/auth/cookies';
import { getDeletionState } from '@/lib/account/deletion-actions';
import { ConsoleSidebarNav } from '@/components/ConsoleSidebarNav';
import { IntercomMessenger } from '@/components/business/IntercomMessenger';
import { BetaBadge } from '@/components/business/BetaBadge';

interface ConsoleLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default async function ConsoleLayout({
  children,
  params: { locale },
}: ConsoleLayoutProps) {
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;

  const hasAccessCookie = cookies().get(ACCESS_COOKIE);
  if (!hasAccessCookie) {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/profile`)}`);
  }

  const deletionState = await getDeletionState();
  if (deletionState.kind === 'unauthorized') {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/profile`)}`);
  }
  if (deletionState.kind === 'error' || deletionState.data.status !== 'active') {
    redirect(`/${resolvedLocale}/account/recovery`);
  }

  const t = await getTranslations('account');
  const tDashboard = await getTranslations('dashboard');
  const tLogs = await getTranslations('logs');

  const countryCode = headers().get('cf-ipcountry');
  const intercomAppId = process.env.NEXT_PUBLIC_INTERCOM_APP_ID;

  return (
    <div className="mx-auto max-w-7xl px-6 py-8">
      <IntercomMessenger appId={intercomAppId} locale={resolvedLocale} countryCode={countryCode} />
      <div className="mb-8 rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 p-1.5 shadow-sm">
        <nav className="flex gap-1" aria-label={t('settings.sidebar.label')}>
          <BetaBadge />
          <ConsoleSidebarNav
            items={[
              { href: `/${resolvedLocale}/dashboard`, label: tDashboard('nav.sidebar') },
              { href: `/${resolvedLocale}/logs`, label: tLogs('nav.sidebar') },
              { href: `/${resolvedLocale}/keys`, label: t('keys.nav.sidebar') },
              { href: `/${resolvedLocale}/settings/profile`, label: t('settings.sidebar.profile') },
              { href: `/${resolvedLocale}/settings/security`, label: t('settings.sidebar.security') },
              { href: `/${resolvedLocale}/settings/data`, label: t('settings.sidebar.data') },
            ]}
            variant="tabs"
          />
        </nav>
      </div>
      <div className="animate-fade-in">{children}</div>
    </div>
  );
}
