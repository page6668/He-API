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

  return <div className="mx-auto max-w-md px-6 py-10">{children}</div>;
}
