// (auth) route group layout — Next.js route groups are excluded from URLs
// (the directory parens are a Next.js convention), so any page under this
// folder serves at `/{locale}/<page>`, not `/{locale}/(auth)/<page>`.
//
// Story 2.2 TS-CONS-012 — this layout detects an `he_access` cookie *by
// presence* (HttpOnly so JS can't read it and we can't parse the JWT here
// either). Real authentication enforcement lives in api-gateway middleware
// (Story 2.5+). A logged-in user that hits any (auth)/* page bounces back
// to the locale root.

import type { ReactNode } from 'react';
import { cookies } from 'next/headers';
import { redirect } from 'next/navigation';

import { defaultLocale, isLocale } from '@/i18n/config';
import { ACCESS_COOKIE } from '@/lib/auth/cookies';
import { Logo } from '@/components/brand/Logo';

interface AuthLayoutProps {
  children: ReactNode;
  params: { locale: string };
}

export default function AuthLayout({ children, params: { locale } }: AuthLayoutProps) {
  // The locale segment is validated by the parent [locale] layout already;
  // we defensively fall back here so a typo in a hard-coded link doesn't
  // explode the page render.
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;

  // Cookie-presence-only check per TS-CONS-012. HttpOnly cookies are not
  // readable from client JS; the gateway is what actually validates the JWT.
  const hasAccessCookie = cookies().get(ACCESS_COOKIE);
  if (hasAccessCookie) {
    redirect(`/${resolvedLocale}/`);
  }

  // 版式(design-system.md key_page_direction.auth):收窄至 400px、左对齐编辑式排版,
  // 顶部一枚品牌印记。内容恒有最大宽度并居中容器 —— 禁止裸贴视口边缘。
  return (
    <div className="mx-auto max-w-[400px] px-6 py-16">
      <header className="mb-8">
        <Logo size={22} />
      </header>
      {children}
    </div>
  );
}
