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
import { cookies, headers } from 'next/headers';
import { redirect } from 'next/navigation';
import { getTranslations } from 'next-intl/server';

import { defaultLocale, isLocale } from '@/i18n/config';
import { ACCESS_COOKIE } from '@/lib/auth/cookies';
import { getDeletionState } from '@/lib/account/deletion-actions';
import { getMyRole } from '@/lib/account/get-my-role';
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

  // Cookie-presence check only (HttpOnly cookies are opaque to JS — the JWT
  // itself is verified by api-gateway). Defence-in-depth pairing with the
  // gateway's JWT middleware (Story 2.2 BR-3.7).
  const hasAccessCookie = cookies().get(ACCESS_COOKIE);
  if (!hasAccessCookie) {
    redirect(`/${resolvedLocale}/signin?return_to=${encodeURIComponent(`/${resolvedLocale}/settings/profile`)}`);
  }

  // Story 2.7 AC4 — recovery-only-session guard (OQ-1). A pending_deletion user
  // authenticates but is BLOCKED from the console: every (console) route bounces
  // to /{locale}/account/recovery (which lives outside this layout, so no
  // redirect loop — BR-4.2). Fail-closed (BR-4.5): if the deletion-state read is
  // unreachable, deny console (redirect to recovery) rather than risk granting a
  // pending account console access. A 401 (token expired) routes to signin.
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

  // AD-003 — 只有管理员才在侧边栏看到「模型定价」入口。role 由网关读库返回
  // (前端无法解 JWT);fail-safe 为 'user',读不到就不显示。写钱拦截仍在网关。
  const role = await getMyRole();

  const navItems = [
    { href: `/${resolvedLocale}/dashboard`, label: tDashboard('nav.sidebar') },
    { href: `/${resolvedLocale}/logs`, label: tLogs('nav.sidebar') },
    { href: `/${resolvedLocale}/keys`, label: t('keys.nav.sidebar') },
    { href: `/${resolvedLocale}/settings/profile`, label: t('settings.sidebar.profile') },
    { href: `/${resolvedLocale}/settings/security`, label: t('settings.sidebar.security') },
    { href: `/${resolvedLocale}/settings/data`, label: t('settings.sidebar.data') },
  ];
  if (role === 'admin') {
    navItems.push({ href: `/${resolvedLocale}/admin/pricing`, label: t('pricing.title') });
    navItems.push({ href: `/${resolvedLocale}/admin/providers`, label: '模型供应商' });
  }

  // Story 10.7 AC2 — Intercom (customer-support) boots on the authed console
  // face only (OQ-6). Region-gate (OQ-2): PRC-region requests do NOT boot —
  // the country code is resolved from the edge (Cloudflare cf-ipcountry).
  // The app id is the PUBLIC NEXT_PUBLIC_INTERCOM_APP_ID (no secret here).
  const countryCode = headers().get('cf-ipcountry');
  const intercomAppId = process.env.NEXT_PUBLIC_INTERCOM_APP_ID;

  return (
    // 固定 240px 导航 + 流式内容区(页面各自用 PageShell 限宽 1080 居中)。
    // 逻辑属性 border-e —— 支持 ar 等 RTL(design-system.md i18n_rtl)。
    <div className="flex min-h-screen bg-paper">
      <IntercomMessenger appId={intercomAppId} locale={resolvedLocale} countryCode={countryCode} />
      <aside
        className="w-60 shrink-0 border-e border-line bg-surface px-4 py-8"
        aria-label={t('settings.sidebar.label')}
      >
        {/* Story 10.8 AC2 (§9.2) — outward "Beta" marking on the authed console
            shell; env-driven, renders null on the GA face (BR-10.8.9). */}
        <div className="mb-4">
          <BetaBadge />
        </div>
        <ConsoleSidebarNav items={navItems} />
      </aside>
      <section className="min-w-0 flex-1">{children}</section>
    </div>
  );
}
