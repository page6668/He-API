import createMiddleware from 'next-intl/middleware';
import type { NextRequest } from 'next/server';
import { locales, defaultLocale } from '@/i18n/config';

const COOKIE_NAME = 'he_locale';
const LOCALE_RE = new RegExp(`^/(${locales.join('|')})(?:/|$)`);

// Custom locale resolution: ONLY read he_locale cookie (NOT Accept-Language).
// next-intl's localeDetection reads both; we suppress Accept-Language by
// always overwriting it, so only the cookie (set by user手动切换) is honoured.
function getLocaleFromCookie(request: NextRequest): string | undefined {
  const val = request.cookies.get(COOKIE_NAME)?.value;
  if (val && locales.includes(val)) return val;
  return undefined;
}

const intlMiddleware = createMiddleware({
  locales: [...locales],
  defaultLocale,
  localePrefix: 'always',
  localeDetection: true,   // needed so it writes cookie when visiting /zh-CN
  localeCookie: { name: COOKIE_NAME },
});

// Story 5.5 BR-PD-7 — the one-time API-key display sub-page carries the
// plaintext in its URL; harden it against intermediate-proxy caching. App
// Router exposes no per-segment header config, so it is applied here.
const CREATED_KEY_PATH = /^\/[A-Za-z-]+\/keys\/[^/]+\/created\/?$/;

export default function middleware(request: NextRequest) {
  // Always suppress Accept-Language so next-intl cannot auto-detect from browser.
  // Result: defaultLocale wins unless user has set he_locale cookie.
  const headers = new Headers(request.headers);
  headers.set('accept-language', 'en');

  // Strip existing locale prefix so createMiddleware re-resolves cleanly
  const pathname = request.nextUrl.pathname;
  const stripped = pathname.replace(LOCALE_RE, '/');
  const modifiedUrl = request.nextUrl.clone();
  modifiedUrl.pathname = stripped === pathname ? '/' : stripped;

  const adaptedRequest = new NextRequest(modifiedUrl, {
    headers,
    method: request.method,
    body: request.body,
    bodyUsed: request.bodyUsed,
    cache: request.cache,
    credentials: request.credentials,
    integrity: request.integrity,
    mode: request.mode,
    redirect: request.redirect,
    referrer: request.referrer,
    referrerPolicy: request.referrerPolicy,
  });

  const response = intlMiddleware(adaptedRequest);

  if (CREATED_KEY_PATH.test(request.nextUrl.pathname)) {
    response.headers.set('Cache-Control', 'no-store, no-cache, must-revalidate, max-age=0');
  }
  return response;
}

export const config = {
  matcher: ['/((?!api|_next|_vercel|.*\\..*).*)'],
};
