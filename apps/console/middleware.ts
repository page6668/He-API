import createMiddleware from 'next-intl/middleware';
import type { NextRequest } from 'next/server';
import { locales, defaultLocale } from '@/i18n/config';

// Cookie name MUST stay literal here (Story 2.1 Architect Q4 ruling: custom name
// 'he_locale', not the framework default). Reflected in
// lib/i18n.ts:COOKIE_NAME export for Server Action use.
const intlMiddleware = createMiddleware({
  locales: [...locales],
  defaultLocale,
  localePrefix: 'always',
  localeDetection: false,
  localeCookie: {
    name: 'he_locale',
  },
});

// Story 5.5 BR-PD-7 — the one-time API-key display sub-page carries the
// plaintext in its URL; harden it against intermediate-proxy caching. App
// Router exposes no per-segment header config, so it is applied here.
const CREATED_KEY_PATH = /^\/[A-Za-z-]+\/keys\/[^/]+\/created\/?$/;

export default function middleware(request: NextRequest) {
  const response = intlMiddleware(request);
  if (CREATED_KEY_PATH.test(request.nextUrl.pathname)) {
    response.headers.set('Cache-Control', 'no-store, no-cache, must-revalidate, max-age=0');
  }
  return response;
}

export const config = {
  matcher: ['/((?!api|_next|_vercel|.*\\..*).*)'],
};
