import { NextResponse } from 'next/server';
import type { NextRequest } from 'next/server';
import { locales, defaultLocale } from '@/i18n/config';

// Story 5.5 BR-PD-7 — the one-time API-key display sub-page carries the
// plaintext in its URL; harden it against intermediate-proxy caching.
const CREATED_KEY_PATH = /^\/[A-Za-z-]+\/keys\/[^/]+\/created\/?$/;
const LOCALE_RE = new RegExp(`^/(${locales.join('|')})(?:/|$)`);
const COOKIE_NAME = 'he_locale';

export default function middleware(request: NextRequest) {
  const pathname = request.nextUrl.pathname;

  // ── 1. Determine locale ──────────────────────────────────────────────────
  // ONLY honour the he_locale cookie. Never read Accept-Language.
  const cookieLocale =
    request.cookies.get(COOKIE_NAME)?.value?.trim().toLowerCase();
  const locale =
    cookieLocale && locales.includes(cookieLocale)
      ? cookieLocale
      : defaultLocale; // always 'en'

  // ── 2. Redirect: pathname has no locale prefix → add locale ─────────────
  // Visiting a locale URL directly (e.g. /zh-CN) is treated as an explicit
  // user choice → set cookie but do NOT further redirect.
  if (!LOCALE_RE.test(pathname)) {
    const url = request.nextUrl.clone();
    url.pathname = `/${locale}${pathname === '/' ? '' : pathname}`;
    const response = NextResponse.redirect(url);
    if (locale !== defaultLocale) {
      response.cookies.set(COOKIE_NAME, locale, {
        path: '/',
        maxAge: 60 * 60 * 24 * 365,
        sameSite: 'lax',
      });
    }
    return response;
  }

  // ── 3. Normal request — add cache headers where needed ──────────────────
  const response = NextResponse.next();
  if (CREATED_KEY_PATH.test(pathname)) {
    response.headers.set(
      'Cache-Control',
      'no-store, no-cache, must-revalidate, max-age=0'
    );
  }
  return response;
}

export const config = {
  matcher: ['/((?!api|_next|_vercel|.*\\..*).*)'],
};
